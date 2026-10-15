package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PaNasMs/module-sdk/maintenance"
	"github.com/PaNasMs/module-sdk/modules"
)

type JobRequest struct {
	RequestID  string `json:"requestId"`
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	Plan       string `json:"plan"`
	Snapshot   string `json:"snapshot"`
	Path       string `json:"path"`
}

func (e *Engine) Start(v JobRequest) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v.RequestID != "" {
		if len(v.RequestID) != 32 {
			return Job{}, errors.New("Invalid request identity")
		}
		if _, err := hex.DecodeString(v.RequestID); err != nil {
			return Job{}, errors.New("Invalid request identity")
		}
	}
	rawRequest, _ := json.Marshal(v)
	sum := sha256.Sum256(rawRequest)
	fingerprint := hex.EncodeToString(sum[:])
	for _, old := range e.state.Jobs {
		if v.RequestID != "" && old.ID == v.RequestID {
			if old.Request != fingerprint {
				return Job{}, errors.New("Request identity was reused for a different operation")
			}
			return old, nil
		}
	}
	j := Job{ID: newID(), Kind: v.Kind, Repository: v.Repository, Plan: v.Plan, Status: "running", Phase: "preparing", Started: time.Now().UTC(), Snapshot: v.Snapshot}
	if v.RequestID != "" {
		j.ID = v.RequestID
	}
	j.Request = fingerprint
	if e.busy || (len(e.state.Recovery) > 0 && v.Kind != "recover") {
		return j, errors.New("An operation or application recovery is pending")
	}
	var p Plan
	if v.Kind == "backup" {
		for _, x := range e.state.Plans {
			if x.ID == v.Plan {
				p = x
			}
		}
		if p.ID == "" {
			return j, errors.New("Plan not found")
		}
		j.Repository = p.Repository
	}
	r, err := e.repo(j.Repository)
	if v.Kind == "recover" {
		err = nil
	}
	if err != nil {
		return j, err
	}
	switch v.Kind {
	case "backup", "check", "recover":
	case "restore", "restoreApplications":
		if !snapshotID(v.Snapshot) {
			return j, errors.New("Select a complete snapshot ID")
		}
		if v.Path != "" && (!filepath.IsAbs(v.Path) || filepath.Clean(v.Path) != v.Path || strings.ContainsAny(v.Path, "\x00\n\r")) {
			return j, errors.New("Invalid snapshot path")
		}
		j.Destination = filepath.Join(e.workRoot(), "restores", j.ID)
	default:
		return j, errors.New("Unsupported operation")
	}
	exclusive := false
	for _, source := range p.Sources {
		exclusive = exclusive || source.Kind == "container" || source.Kind == "platform"
	}
	release := func() {}
	if !exclusive {
		release, err = maintenance.Acquire()
		if err != nil {
			return j, err
		}
	}
	e.state.Jobs = append(e.state.Jobs, j)
	if err = e.save(); err != nil {
		e.state.Jobs = e.state.Jobs[:len(e.state.Jobs)-1]
		release()
		return j, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 48*time.Hour)
	e.cancel = cancel
	e.busy = true
	go func() {
		defer release()
		defer cancel()
		watch, stopWatch := context.WithCancel(ctx)
		checked := make(chan error, 1)
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-watch.Done():
					checked <- nil
					return
				case <-ticker.C:
					if j.Kind != "recover" {
						if err := e.checkWorkspace(); err != nil {
							cancel()
							checked <- err
							return
						}
					}
				}
			}
		}()
		result := e.work(ctx, j, p, r, v)
		stopWatch()
		e.finish(j.ID, errors.Join(result, <-checked))
	}()
	return j, nil
}
func (e *Engine) phase(id, phase string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.state.Jobs {
		if e.state.Jobs[i].ID == id {
			e.state.Jobs[i].Phase = phase
		}
	}
	return e.save()
}
func (e *Engine) finish(id string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.state.Jobs {
		j := &e.state.Jobs[i]
		if j.ID != id {
			continue
		}
		now := time.Now().UTC()
		j.Finished = &now
		j.Status = "succeeded"
		j.Phase = "finished"
		if err != nil {
			j.Status = "failed"
			j.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				j.Status = "cancelled"
			}
		}
		for k := range e.state.Plans {
			p := &e.state.Plans[k]
			if p.ID == j.Plan {
				p.NextRun = now.Add(time.Duration(max(p.EveryHours, 1)) * time.Hour)
			}
		}
	}
	if saveErr := e.save(); saveErr != nil {
		for i := range e.state.Jobs {
			if e.state.Jobs[i].ID == id {
				e.state.Jobs[i].Status = "failed"
				e.state.Jobs[i].Error = "Cannot persist the operation result; inspect storage before retrying"
			}
		}
	}
	e.busy = false
	e.cancel = nil
}
func (e *Engine) Cancel(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.state.Jobs {
		j := &e.state.Jobs[i]
		if j.ID == id && j.Status == "running" && e.cancel != nil {
			e.cancel()
			j.Phase = "cancelling"
			return e.save()
		}
	}
	return errors.New("No active operation with this ID")
}
func (e *Engine) work(ctx context.Context, j Job, p Plan, r Repository, v JobRequest) error {
	if j.Kind == "recover" {
		return e.RecoverApplications()
	}
	if err := e.checkWorkspace(); err != nil {
		return err
	}
	if r.Provider == "github" {
		var err error
		if j.Kind == "backup" {
			if _, err = verifyGithub(ctx, r); err != nil {
				return err
			}
			r, err = e.githubLocal(r, j.ID)
			if err != nil {
				return err
			}
			if _, err = e.restic(ctx, r, nil, "init"); err != nil {
				return err
			}
		} else {
			r, err = e.downloadGithub(ctx, r, v.Snapshot)
			if err != nil {
				return err
			}
		}
		defer os.RemoveAll(r.Local)
	}
	raw, err := e.restic(ctx, r, nil, "cat", "config")
	if err != nil {
		return err
	}
	var config struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &config) != nil || (r.Provider != "github" && config.ID != r.RepositoryID) {
		return errors.New("Repository identity changed; reconnect the original repository")
	}
	switch j.Kind {
	case "check":
		if err = e.phase(j.ID, "checking"); err != nil {
			return err
		}
		_, err = e.restic(ctx, r, nil, "check", "--read-data")
		return err
	case "restore", "restoreApplications":
		if err = e.phase(j.ID, "restoring"); err != nil {
			return err
		}
		if v.Path != "" {
			listing, listErr := e.restic(ctx, r, nil, "ls", v.Snapshot, "--", v.Path)
			if listErr != nil {
				return listErr
			}
			found := false
			for _, line := range strings.Split(string(listing), "\n") {
				var node struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(line), &node) == nil && node.Path == v.Path {
					found = true
				}
			}
			if !found {
				return errors.New("The selected file is not present in this snapshot")
			}
		}
		if err = os.MkdirAll(j.Destination, 0700); err != nil {
			return err
		}
		args := []string{"restore", v.Snapshot, "--target", j.Destination, "--verify"}
		if v.Path != "" {
			pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[").Replace(v.Path)
			args = append(args, "--include", pattern)
		}
		_, err = e.restic(ctx, r, nil, args...)
		if err == nil && v.Path != "" {
			if info, statErr := os.Stat(filepath.Join(j.Destination, v.Path)); statErr == nil && info.Mode().IsRegular() {
				e.mu.Lock()
				for i := range e.state.Jobs {
					if e.state.Jobs[i].ID == j.ID {
						e.state.Jobs[i].File = strings.TrimPrefix(v.Path, "/")
					}
				}
				err = e.save()
				e.mu.Unlock()
			}
		}
		if err == nil && j.Kind == "restoreApplications" {
			err = e.restoreApplications(ctx, j)
		}
		return err
	default:
		return e.backup(ctx, j, p, r)
	}
}
func (e *Engine) backup(ctx context.Context, j Job, p Plan, r Repository) (result error) {
	stage := filepath.Join(e.workRoot(), "staging", p.ID, j.ID)
	if err := os.MkdirAll(stage, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return errors.New("Previous staging data requires review before retrying; it has been preserved")
	}
	complete := false
	defer func() {
		if complete {
			if err := os.RemoveAll(stage); err != nil {
				result = errors.Join(result, err)
			}
		}
	}()

	exclusive := false
	for _, source := range p.Sources {
		exclusive = exclusive || source.Kind == "platform" || source.Kind == "container"
	}
	unlock := func() {}
	if exclusive {
		var lockErr error
		unlock, lockErr = exclusiveMaintenance(ctx)
		if lockErr != nil {
			return lockErr
		}
	}
	unlocked := false
	defer func() {
		result = errors.Join(result, e.RecoverApplications())
		if !unlocked {
			unlock()
		}
	}()
	paths, err := e.prepare(ctx, j, p, r, stage)
	if err != nil {
		return err
	}
	if err = e.RecoverApplications(); err != nil {
		return err
	}
	unlock()
	unlocked = true
	if exclusive {
		release, lockErr := maintenance.Acquire()
		if lockErr != nil {
			return lockErr
		}
		defer release()
	}
	metadata := map[string]any{"format": 1, "plan": p, "created": time.Now().UTC(), "stage": stage, "restore": "Restore into a new directory, then review recovery.json. System configuration is never applied to the running NAS automatically."}
	if err = atomicJSON(filepath.Join(stage, "recovery.json"), metadata); err != nil {
		return err
	}
	paths = append(paths, stage)
	if err = e.phase(j.ID, "backingUp"); err != nil {
		return err
	}
	args := []string{"backup", "--tag", "panasms:" + p.ID, "--group-by", "tags", "--one-file-system", "--"}
	args = append(args, paths...)
	raw, err := e.restic(ctx, r, nil, args...)
	if err != nil {
		return err
	}
	var snapshot string
	for _, line := range strings.Split(string(raw), "\n") {
		var msg struct {
			Snapshot string `json:"snapshot_id"`
		}
		if json.Unmarshal([]byte(line), &msg) == nil && msg.Snapshot != "" {
			snapshot = msg.Snapshot
		}
	}
	if !snapshotID(snapshot) {
		return errors.New("Backup did not return a complete snapshot identity")
	}
	e.mu.Lock()
	for i := range e.state.Jobs {
		if e.state.Jobs[i].ID == j.ID {
			e.state.Jobs[i].Snapshot = snapshot
		}
	}
	err = e.save()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	for _, source := range p.Sources {
		if source.Kind == "files" || source.Kind == "sqlite" {
			mount, checkErr := e.mount(source.Path)
			if checkErr != nil || mount != source.Mount {
				return errors.New("Source storage changed during backup; no old versions were removed")
			}
		}
	}
	if err = e.RecoverApplications(); err != nil {
		return err
	}
	if err = e.phase(j.ID, "checking"); err != nil {
		return err
	}
	if _, err = e.restic(ctx, r, nil, "check"); err != nil {
		return err
	}
	if err = e.phase(j.ID, "retention"); err != nil {
		return err
	}
	if r.Provider == "github" {
		j.Snapshot = snapshot
		if err = e.uploadGithub(ctx, r, j, p); err != nil {
			return err
		}
		if err = e.githubRetention(ctx, r, p); err != nil {
			return err
		}
	} else {
		_, err = e.restic(ctx, r, nil, "forget", "--tag", "panasms:"+p.ID, "--group-by", "tags", "--keep-last", strconv.Itoa(p.KeepLast), "--keep-daily", strconv.Itoa(p.KeepDaily), "--keep-weekly", strconv.Itoa(p.KeepWeekly), "--prune")
		if err != nil {
			return fmt.Errorf("Backup %s exists, but retention failed: %w", snapshot, err)
		}
	}
	complete = true
	return nil
}
func (e *Engine) Schedule(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if !modules.Enabled("backup") {
				continue
			}
			state := e.State()
			for _, p := range state.Plans {
				if p.Enabled && p.EveryHours > 0 && !p.NextRun.After(now) {
					_, _ = e.Start(JobRequest{Kind: "backup", Plan: p.ID})
					break
				}
			}
		}
	}
}
