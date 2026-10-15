package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Repository struct {
	Local    string `json:"-"`
	Provider string `json:"provider"`
	Grant    string `json:"grant,omitempty"`
	Owner    string `json:"owner,omitempty"`

	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Mount        string `json:"mount"`
	RepositoryID string `json:"repositoryId"`
}
type Source struct {
	Volumes map[string]string `json:"volumes,omitempty"`
	Kind    string            `json:"kind"`
	Path    string            `json:"path"`
	Mount   string            `json:"mount,omitempty"`
}
type Plan struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Repository    string    `json:"repository"`
	Sources       []Source  `json:"sources"`
	EveryHours    int       `json:"everyHours"`
	KeepLast      int       `json:"keepLast"`
	KeepDaily     int       `json:"keepDaily"`
	KeepWeekly    int       `json:"keepWeekly"`
	Enabled       bool      `json:"enabled"`
	AllowDowntime bool      `json:"allowDowntime"`
	NextRun       time.Time `json:"nextRun"`
}
type Job struct {
	File        string     `json:"file,omitempty"`
	Request     string     `json:"request,omitempty"`
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Plan        string     `json:"plan,omitempty"`
	Repository  string     `json:"repository"`
	Status      string     `json:"status"`
	Phase       string     `json:"phase"`
	Started     time.Time  `json:"started"`
	Finished    *time.Time `json:"finished,omitempty"`
	Error       string     `json:"error,omitempty"`
	Snapshot    string     `json:"snapshot,omitempty"`
	Destination string     `json:"destination,omitempty"`
}
type Workspace struct {
	Path  string `json:"path"`
	Mount string `json:"mount"`
}
type State struct {
	Workspace    Workspace    `json:"workspace"`
	Repositories []Repository `json:"repositories"`
	Plans        []Plan       `json:"plans"`
	Jobs         []Job        `json:"jobs"`
	Recovery     []string     `json:"recovery"`
}
type Engine struct {
	mu      sync.Mutex
	queryMu sync.Mutex
	root    string
	state   State
	busy    bool
	cancel  func()
	run     Command
	mount   func(string) (string, error)
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func atomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(path, append(b, '\n'))
}
func atomicFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func New(root string) (*Engine, error) {
	for _, p := range []string{root, filepath.Join(root, "keys"), filepath.Join(root, "staging"), filepath.Join(root, "restores")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(p, 0700); err != nil {
			return nil, err
		}
	}
	e := &Engine{root: root, run: command, mount: mountIdentity, state: State{Repositories: []Repository{}, Plans: []Plan{}, Jobs: []Job{}, Recovery: []string{}}}
	b, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err == nil {
		err = json.Unmarshal(b, &e.state)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	for i := range e.state.Jobs {
		j := &e.state.Jobs[i]
		if j.Status == "running" {
			j.Status = "interrupted"
			j.Error = "Service restarted. Inspect the repository before retrying; restored files are retained."
			now := time.Now().UTC()
			j.Finished = &now
		}
	}
	return e, e.save()
}
func (e *Engine) save() error { return atomicJSON(filepath.Join(e.root, "state.json"), e.state) }
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.state)
	var s State
	_ = json.Unmarshal(b, &s)
	return s
}
func (e *Engine) Active() int32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy || len(e.state.Recovery) > 0 {
		return 1
	}
	return 0
}
func (e *Engine) repo(id string) (Repository, error) {
	for _, r := range e.state.Repositories {
		if r.ID == id {
			return r, nil
		}
	}
	return Repository{}, errors.New("Repository not found")
}
func validName(s string) bool {
	return len(strings.TrimSpace(s)) > 0 && len(s) <= 120 && !strings.ContainsAny(s, "\x00\n\r")
}
func within(path, base string) bool { return path == base || strings.HasPrefix(path, base+"/") }
func cleanPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\n\r") {
		return errors.New("Choose an absolute, canonical path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("Symbolic links are not accepted as source or repository paths")
	}
	return nil
}
func (e *Engine) SavePlan(p Plan) (Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy {
		return p, errors.New("Wait for the active operation")
	}
	r, err := e.repo(p.Repository)
	if err != nil {
		return p, err
	}
	if !validName(p.Name) || len(p.Sources) == 0 || len(p.Sources) > 32 || p.EveryHours < 0 || p.EveryHours > 8760 || p.KeepLast < 1 || p.KeepLast > 1000 || p.KeepDaily < 0 || p.KeepDaily > 3650 || p.KeepWeekly < 0 || p.KeepWeekly > 520 {
		return p, errors.New("Provide a name, sources, a valid interval and at least one retained copy")
	}
	if p.ID != "" {
		found := false
		for _, old := range e.state.Plans {
			if old.ID == p.ID {
				found = true
			}
		}
		if !found {
			return p, errors.New("Plan not found")
		}
	} else {
		p.ID = newID()
	}
	for i := range p.Sources {
		s := &p.Sources[i]
		if r.Provider == "github" && s.Kind != "platform" {
			return p, errors.New("GitHub accepts platform configuration only")
		}
		switch s.Kind {
		case "files", "sqlite":
			if err = cleanPath(s.Path); err != nil {
				return p, err
			}
			for _, denied := range []string{"/proc", "/sys", "/dev", "/run", e.root, e.workRoot()} {
				if within(s.Path, denied) || within(denied, s.Path) {
					return p, errors.New("Select a data folder, not a system or backup working directory")
				}
			}
			if within(r.Path, s.Path) || within(s.Path, r.Path) {
				return p, errors.New("Backup source and repository must not overlap")
			}
			s.Mount, err = e.mount(s.Path)
			if err != nil {
				return p, err
			}
		case "postgres", "mysql", "container":
			if !validContainer(s.Path) {
				return p, errors.New("Choose a container ID or name")
			}
			if s.Kind == "container" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				containers, _, inspectErr := e.inspect(ctx, s.Path)
				cancel()
				if inspectErr != nil || len(containers) != 1 {
					return p, errors.New("Application is unavailable")
				}
				s.Path = containers[0].ID
				s.Volumes = map[string]string{}
				for _, mount := range containers[0].Mounts {
					identity, mountErr := e.mount(mount.Source)
					if mountErr != nil {
						return p, mountErr
					}
					s.Volumes[mount.Source] = identity
				}
			}
			if s.Kind == "container" && !p.AllowDowntime {
				return p, errors.New("Application backup requires permission to stop the selected containers temporarily")
			}
		case "platform":
			s.Path = "platform"
		default:
			return p, errors.New("Unsupported source type")
		}
	}
	p.NextRun = time.Now().UTC().Add(time.Duration(p.EveryHours) * time.Hour)
	old := append([]Plan(nil), e.state.Plans...)
	updated := false
	for i := range e.state.Plans {
		if e.state.Plans[i].ID == p.ID {
			e.state.Plans[i] = p
			updated = true
		}
	}
	if !updated {
		e.state.Plans = append(e.state.Plans, p)
	}
	if err = e.save(); err != nil {
		e.state.Plans = old
	}
	return p, err
}
func validContainer(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && strings.ContainsRune("_.-", c)) {
			return false
		}
	}
	return true
}
