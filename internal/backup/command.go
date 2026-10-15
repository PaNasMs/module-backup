package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Command func(context.Context, io.Writer, string, ...string) ([]byte, error)
type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 32<<20 {
		return 0, errors.New("Command output exceeded limit")
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, dest io.Writer, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out := &boundedBuffer{}
	if dest == nil {
		cmd.Stdout = out
	} else {
		cmd.Stdout = dest
	}
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && name == "restic" {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case 3:
				return nil, errors.New("Backup is incomplete because some source files could not be read. Old versions were preserved")
			case 10:
				return nil, errors.New("Backup repository is missing")
			case 11:
				return nil, errors.New("Backup repository is locked by another operation. Wait for it to finish")
			case 12:
				return nil, errors.New("Repository password is incorrect")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s failed (%v). Check permissions, available space and connectivity", name, err)
	}
	return out.Bytes(), nil
}
func mountIdentity(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := command(ctx, nil, "findmnt", "--json", "--first-only", "--target", path, "--output", "TARGET,SOURCE,FSTYPE,UUID,FSROOT")
	if err != nil {
		return "", err
	}
	var value struct {
		Filesystems []map[string]any `json:"filesystems"`
	}
	if json.Unmarshal(b, &value) != nil || len(value.Filesystems) != 1 {
		return "", errors.New("Cannot identify storage")
	}
	m := value.Filesystems[0]
	if uuid, ok := m["uuid"].(string); ok && uuid != "" {
		delete(m, "source")
	}
	if m["fstype"] == "autofs" {
		return "", errors.New("Mount the storage before selecting it")
	}
	raw, _ := json.Marshal(m)
	return string(raw), nil
}
func (e *Engine) checkRepository(r Repository) error {
	if err := cleanPath(r.Path); err != nil {
		return errors.New("Repository is unavailable; reconnect the original storage")
	}
	m, err := e.mount(r.Path)
	if err != nil || m != r.Mount {
		return errors.New("Repository storage changed or disconnected; reconnect the original storage")
	}
	return nil
}
func (e *Engine) restic(ctx context.Context, r Repository, dest io.Writer, args ...string) ([]byte, error) {
	location := r.Path
	base := []string{}
	if r.Provider == "drive" || r.Provider == "dropbox" {
		config, cleanup, err := e.cloudConfig(ctx, r)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		location = "rclone:cloud:" + r.Path
		base = append(base, "-o", "rclone.args=serve restic --stdio --config="+config, "-o", "rclone.timeout=60s")
	} else if r.Provider == "github" {
		if r.Local == "" {
			return nil, errors.New("GitHub archive is not loaded")
		}
		location = r.Local
	} else {
		if err := e.checkRepository(r); err != nil {
			return nil, err
		}
		directory, err := os.Open(r.Path)
		if err != nil {
			return nil, err
		}
		defer directory.Close()
		location = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), directory.Fd())
	}
	base = append(base, "--repo", location, "--password-file", filepath.Join(e.root, "keys", r.ID), "--cache-dir", filepath.Join(e.root, "cache"), "--json")
	return e.run(ctx, dest, "restic", append(base, args...)...)
}

type RepositoryRequest struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Grant    string `json:"grant"`
	Owner    string `json:"-"`

	Name     string `json:"name"`
	Path     string `json:"path"`
	Password string `json:"password"`
	Create   bool   `json:"create"`
}

func (e *Engine) AddRepository(ctx context.Context, v RepositoryRequest) (Repository, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := Repository{ID: newID(), Name: strings.TrimSpace(v.Name), Path: v.Path, Provider: v.Provider, Grant: v.Grant, Owner: v.Owner}
	if e.busy {
		return r, errors.New("Wait for the active operation")
	}
	if !validName(r.Name) || len(v.Password) < 8 || strings.ContainsAny(v.Password, "\n\r\x00") {
		return r, errors.New("Provide a name and a repository password of at least eight characters")
	}
	var err error
	var previous Repository
	if v.ID != "" {
		previous, err = e.repo(v.ID)
		if err != nil {
			return r, err
		}
		r.ID = previous.ID
		if v.Create || previous.Provider != r.Provider {
			return r, errors.New("Reconnect the existing repository with the same provider")
		}
	}
	for _, old := range e.state.Repositories {
		if old.ID != r.ID && old.Provider == r.Provider && r.Provider != "local" && old.Path == r.Path && old.Owner == r.Owner {
			return r, errors.New("Repository is already connected")
		}
	}
	if r.Provider == "" {
		r.Provider = "local"
	}
	if r.Provider == "local" {
		if v.Create {
			if _, statErr := os.Lstat(r.Path); os.IsNotExist(statErr) {
				if !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path {
					return r, errors.New("Invalid repository path")
				}
				if err = cleanPath(filepath.Dir(r.Path)); err != nil {
					return r, err
				}
				if err = os.Mkdir(r.Path, 0700); err != nil {
					return r, err
				}
			}
		}
		if err = cleanPath(r.Path); err != nil {
			return r, err
		}
		if within(r.Path, e.root) || within(e.root, r.Path) || within(r.Path, e.workRoot()) || within(e.workRoot(), r.Path) {
			return r, errors.New("Choose a separate backup location")
		}
		for _, old := range e.state.Repositories {
			if old.ID != r.ID && old.Provider == "local" && (within(r.Path, old.Path) || within(old.Path, r.Path)) {
				return r, errors.New("Repository overlaps another repository")
			}
		}
		for _, p := range e.state.Plans {
			for _, source := range p.Sources {
				if source.Kind == "files" && (within(r.Path, source.Path) || within(source.Path, r.Path)) {
					return r, errors.New("Repository overlaps a backup source")
				}
			}
		}
		r.Mount, err = e.mount(r.Path)
		if err != nil {
			return r, err
		}
		if v.Create {
			entries, err := os.ReadDir(r.Path)
			if err != nil {
				return r, err
			}
			if len(entries) != 0 {
				return r, errors.New("Choose an empty folder")
			}
		}
	} else if r.Provider == "drive" || r.Provider == "dropbox" {
		if !cloudPath(r.Path) || r.Grant == "" || r.Owner == "" {
			return r, errors.New("Choose a cloud folder and authorize this account")
		}
		if _, err = token(ctx, r); err != nil {
			return r, err
		}
	} else if r.Provider == "github" {
		r.RepositoryID, err = verifyGithub(ctx, r)
		if err != nil {
			return r, err
		}
	} else {
		return r, errors.New("Unsupported provider")
	}
	key := filepath.Join(e.root, "keys", r.ID)
	oldKey, keyErr := os.ReadFile(key)
	if keyErr != nil && !os.IsNotExist(keyErr) {
		return r, keyErr
	}
	if err = atomicFile(key, []byte(v.Password+"\n")); err != nil {
		return r, err
	}
	committed := false
	defer func() {
		if !committed {
			if len(oldKey) > 0 {
				_ = atomicFile(key, oldKey)
			} else {
				_ = os.Remove(key)
			}
		}
	}()
	if r.Provider == "github" {
		if v.Create {
			releases, listErr := githubReleases(ctx, r)
			if listErr != nil {
				return r, listErr
			}
			if len(releases) > 0 {
				return r, errors.New("Existing backups found; choose Connect existing repository with the original password")
			}
		}
		if !v.Create {
			downloaded, downloadErr := e.downloadGithub(ctx, r, "")
			if downloadErr != nil {
				return r, downloadErr
			}
			defer os.RemoveAll(downloaded.Local)
			if _, err = e.restic(ctx, downloaded, nil, "cat", "config"); err != nil {
				return r, err
			}
		}
	} else {
		if v.Create {
			if _, err = e.restic(ctx, r, nil, "init"); err != nil {
				return r, err
			}
		}
		raw, err := e.restic(ctx, r, nil, "cat", "config")
		if err != nil {
			return r, err
		}
		var config struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &config) != nil || len(config.ID) != 64 {
			return r, errors.New("Invalid repository configuration")
		}
		r.RepositoryID = config.ID

	}
	if previous.ID != "" && previous.RepositoryID != r.RepositoryID {
		return r, errors.New("Repository identity does not match the original backup")
	}
	before := append([]Repository(nil), e.state.Repositories...)
	if previous.ID == "" {
		e.state.Repositories = append(e.state.Repositories, r)
	} else {
		for i := range e.state.Repositories {
			if e.state.Repositories[i].ID == r.ID {
				e.state.Repositories[i] = r
			}
		}
	}
	if err = e.save(); err != nil {
		e.state.Repositories = before
		return r, err
	}
	committed = true
	return r, nil
}
func (e *Engine) query(ctx context.Context, id string, args ...string) ([]byte, error) {
	e.mu.Lock()
	r, err := e.repo(id)
	if err != nil || e.busy {
		e.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("Wait for the active operation")
	}
	e.busy = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.busy = false; e.mu.Unlock() }()
	if r.Provider == "github" {
		if args[0] == "snapshots" {
			return e.githubSnapshots(ctx, r)
		}
		r, err = e.downloadGithub(ctx, r, args[1])
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(r.Local)
	}
	return e.restic(ctx, r, nil, args...)
}

func snapshotID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
