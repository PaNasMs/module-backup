package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func (e *Engine) workRoot() string {
	if e.state.Workspace.Path != "" {
		return e.state.Workspace.Path
	}
	return e.root
}

func (e *Engine) checkWorkspace() error {
	path := e.workRoot()
	if err := cleanPath(path); err != nil {
		return err
	}
	if e.state.Workspace.Mount != "" {
		mount, err := e.mount(path)
		if err != nil || mount != e.state.Workspace.Mount {
			return errors.New("Working disk disconnected or changed; reconnect it before continuing")
		}
	}
	var space syscall.Statfs_t
	if err := syscall.Statfs(path, &space); err != nil {
		return err
	}
	if space.Bavail*uint64(space.Bsize) < 2<<30 {
		return errors.New("Working disk needs at least 2 GiB of free reserve space. Choose a larger disk in Recovery")
	}
	return nil
}

func (e *Engine) SetWorkspace(parent string) (Workspace, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy || len(e.state.Recovery) > 0 {
		return Workspace{}, errors.New("Wait for the active operation or application recovery")
	}
	if err := cleanPath(parent); err != nil {
		return Workspace{}, err
	}
	for ancestor := parent; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Stat(ancestor)
		if err != nil {
			return Workspace{}, err
		}
		owner := info.Sys().(*syscall.Stat_t).Uid
		if (owner != 0 && owner != uint32(os.Geteuid())) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return Workspace{}, fmt.Errorf("Choose a private folder on a local disk; parent %s must be owned by root and not writable by other users", ancestor)
		}
		if ancestor == "/" {
			break
		}
	}

	path := filepath.Join(parent, "panasms-backup-work")
	for _, denied := range []string{"/proc", "/sys", "/dev", "/run", e.root} {
		if within(path, denied) || within(denied, path) {
			return Workspace{}, errors.New("Choose a data disk folder")
		}
	}
	for _, r := range e.state.Repositories {
		if r.Provider == "local" && (within(path, r.Path) || within(r.Path, path)) {
			return Workspace{}, errors.New("Working folder and repository must not overlap")
		}
	}
	for _, p := range e.state.Plans {
		for _, s := range p.Sources {
			if s.Kind == "files" || s.Kind == "sqlite" {
				if within(path, s.Path) || within(s.Path, path) {
					return Workspace{}, errors.New("Working folder overlaps a backup source")
				}
			}
			for volume := range s.Volumes {
				if within(path, volume) || within(volume, path) {
					return Workspace{}, errors.New("Working folder overlaps application data")
				}
			}
		}
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return Workspace{}, err
	}
	if err := cleanPath(path); err != nil {
		return Workspace{}, err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return Workspace{}, err
	}
	mount, err := e.mount(path)
	if err != nil {
		return Workspace{}, err
	}
	old := e.state.Workspace
	e.state.Workspace = Workspace{Path: path, Mount: mount}
	if err = e.checkWorkspace(); err == nil {
		err = e.save()
	}
	if err != nil {
		e.state.Workspace = old
		return Workspace{}, err
	}
	return e.state.Workspace, nil
}
