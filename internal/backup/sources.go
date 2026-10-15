package backup

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed recover.py
var recoveryTool []byte

type Container struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
		Paused   bool `json:"Paused"`
	} `json:"State"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

func (e *Engine) inspect(ctx context.Context, names ...string) ([]Container, []byte, error) {
	args := append([]string{"inspect", "--type", "container"}, names...)
	raw, err := e.run(ctx, nil, "docker", args...)
	if err != nil {
		return nil, nil, err
	}
	var list []Container
	if err = json.Unmarshal(raw, &list); err != nil {
		return nil, nil, err
	}
	return list, raw, nil
}
func (e *Engine) Containers(ctx context.Context) ([]Container, error) {
	raw, err := e.run(ctx, nil, "docker", "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		return []Container{}, nil
	}
	list, _, err := e.inspect(ctx, ids...)
	return list, err
}
func (e *Engine) RecoverApplications() error {
	e.mu.Lock()
	ids := append([]string(nil), e.state.Recovery...)
	e.mu.Unlock()
	var failures []error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := e.run(ctx, nil, "docker", "start", id)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("Could not restart application %s: %w", id, err))
			continue
		}
		e.mu.Lock()
		for i, x := range e.state.Recovery {
			if x == id {
				e.state.Recovery = append(e.state.Recovery[:i], e.state.Recovery[i+1:]...)
				break
			}
		}
		err = e.save()
		e.mu.Unlock()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (e *Engine) capture(ctx context.Context, path, command string, args ...string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = e.run(ctx, f, command, args...)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	return err
}

const sqliteBackup = `import sqlite3,sys,urllib.parse
source=sqlite3.connect('file:'+urllib.parse.quote(sys.argv[1])+'?mode=ro',uri=True,timeout=30)
target=sqlite3.connect(sys.argv[2])
source.backup(target)
assert target.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
target.close()
source.close()
`

func (e *Engine) prepare(ctx context.Context, j Job, p Plan, r Repository, stage string) ([]string, error) {
	paths := []string{}
	names := []string{}
	for _, s := range p.Sources {
		if s.Kind == "container" {
			names = append(names, s.Path)
		}
	}
	if len(names) > 0 {
		if !p.AllowDowntime {
			return nil, errors.New("Application downtime was not approved")
		}
		selected, raw, err := e.inspect(ctx, names...)
		if err != nil {
			return nil, err
		}
		if len(selected) != len(names) {
			return nil, errors.New("Application inventory changed")
		}
		all, err := e.Containers(ctx)
		if err != nil {
			return nil, err
		}
		selectedIDs := map[string]bool{}
		for _, c := range selected {
			selectedIDs[c.ID] = true
		}
		for _, c := range selected {
			if c.State.Paused {
				return nil, errors.New("Resume paused containers before creating an application backup")
			}
			expected := map[string]string{}
			for _, source := range p.Sources {
				if source.Kind == "container" && source.Path == c.ID {
					expected = source.Volumes
				}
			}
			if len(expected) != len(c.Mounts) {
				return nil, errors.New("Application mounts changed; review the backup plan")
			}
			for _, m := range c.Mounts {
				current, mountErr := e.mount(m.Source)
				if mountErr != nil || expected[m.Source] == "" || current != expected[m.Source] {
					return nil, errors.New("Application storage changed or disconnected")
				}

				if m.Type != "bind" && m.Type != "volume" {
					return nil, errors.New("Only bind mounts and local Docker volumes are supported")
				}
				if err = cleanPath(m.Source); err != nil {
					return nil, err
				}
				if within(r.Path, m.Source) || within(m.Source, r.Path) || within(e.root, m.Source) || within(m.Source, e.root) || within(e.workRoot(), m.Source) || within(m.Source, e.workRoot()) || m.Source == "/" {
					return nil, errors.New("Application storage overlaps the backup repository or system working directory")
				}
				for _, other := range all {
					if selectedIDs[other.ID] || !other.State.Running {
						continue
					}
					for _, om := range other.Mounts {
						if om.RW && (within(m.Source, om.Source) || within(om.Source, m.Source)) {
							return nil, errors.New("Another running container writes to the same storage; include it in this plan")
						}
					}
				}

			}
		}
		if err = atomicFile(filepath.Join(stage, "containers.json"), raw); err != nil {
			return nil, err
		}
		if err = e.phase(j.ID, "stoppingApplications"); err != nil {
			return nil, err
		}
		for _, c := range selected {
			if !c.State.Running {
				continue
			}
			e.mu.Lock()
			e.state.Recovery = append(e.state.Recovery, c.ID)
			err = e.save()
			e.mu.Unlock()
			if err != nil {
				return nil, err
			}
			if _, err = e.run(ctx, nil, "docker", "stop", "--time", "60", c.ID); err != nil {
				return nil, err
			}
		}
		for _, c := range selected {
			stopped, _, inspectErr := e.inspect(ctx, c.ID)
			if inspectErr != nil || len(stopped) != 1 || stopped[0].State.Running || stopped[0].State.ExitCode == 137 {
				return nil, errors.New("Application did not stop")
			}
			if err = e.capture(ctx, filepath.Join(stage, "rootfs-"+c.ID+".tar"), "docker", "container", "export", c.ID); err != nil {
				return nil, err
			}
		}
		volumeCopies := map[string]string{}
		if err = os.Mkdir(filepath.Join(stage, "volumes"), 0700); err != nil {
			return nil, err
		}
		for _, c := range selected {
			for _, mount := range c.Mounts {
				if _, exists := volumeCopies[mount.Source]; exists {
					continue
				}
				relative := filepath.Join("volumes", fmt.Sprintf("%d", len(volumeCopies)))
				if _, err = e.run(ctx, nil, "cp", "--archive", "--reflink=auto", "--one-file-system", "--", mount.Source, filepath.Join(stage, relative)); err != nil {
					return nil, err
				}
				volumeCopies[mount.Source] = relative
			}
		}
		if err = atomicJSON(filepath.Join(stage, "volumes.json"), volumeCopies); err != nil {
			return nil, err
		}
		images := map[string]bool{}
		for _, c := range selected {
			images[c.Image] = true
		}
		imageArgs := []string{"image", "save"}
		for id := range images {
			imageArgs = append(imageArgs, id)
		}
		if err = e.capture(ctx, filepath.Join(stage, "images.tar"), "docker", imageArgs...); err != nil {
			return nil, err
		}
	}
	for i, s := range p.Sources {
		if s.Kind == "files" || s.Kind == "sqlite" {
			if err := cleanPath(s.Path); err != nil {
				return nil, err
			}
			m, err := e.mount(s.Path)
			if err != nil || m != s.Mount {
				return nil, errors.New("Source storage changed or disconnected")
			}
		}
		target := filepath.Join(stage, fmt.Sprintf("database-%d", i))
		switch s.Kind {
		case "files":
			paths = append(paths, s.Path)
		case "sqlite":
			if _, err := e.run(ctx, nil, "python3", "-c", sqliteBackup, s.Path, target+".sqlite"); err != nil {
				return nil, err
			}
		case "postgres":
			if err := e.capture(ctx, target+".sql", "docker", "exec", s.Path, "sh", "-c", `exec pg_dumpall -U "${POSTGRES_USER:-postgres}"`); err != nil {
				return nil, err
			}
		case "mysql":
			if err := e.capture(ctx, target+".sql", "docker", "exec", s.Path, "sh", "-c", `export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-$MARIADB_ROOT_PASSWORD}"; if command -v mariadb-dump >/dev/null; then exec mariadb-dump --all-databases --lock-all-tables --routines --events --triggers -uroot; else exec mysqldump --all-databases --lock-all-tables --routines --events --triggers -uroot; fi`); err != nil {
				return nil, err
			}
		case "platform":
			if err := e.platform(ctx, stage); err != nil {
				return nil, err
			}
		}
	}
	for _, source := range p.Sources {
		if source.Kind == "container" {
			for path, original := range source.Volumes {
				current, mountErr := e.mount(path)
				if mountErr != nil || current != original {
					return nil, errors.New("Application storage changed during capture")
				}
			}
			current, _, inspectErr := e.inspect(ctx, source.Path)
			if inspectErr != nil || len(current) != 1 || current[0].State.Running {
				return nil, errors.New("Application restarted during capture; consistency cannot be guaranteed")
			}
		}
	}
	return paths, nil
}
func (e *Engine) platform(ctx context.Context, stage string) error {
	if err := atomicFile(filepath.Join(stage, "recover.py"), recoveryTool); err != nil {
		return err
	}
	dir := filepath.Join(stage, "platform")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	system := map[string]string{}
	release, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(release), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			if key == "ID" {
				system["os"] = strings.Trim(value, "\"")
			}
			if key == "VERSION_ID" {
				system["version"] = strings.Trim(value, "\"")
			}
		}
	}
	version, err := os.ReadFile("/usr/lib/panasms/VERSION")
	if err != nil {
		return err
	}
	system["core"] = strings.TrimSpace(string(version))
	if err = atomicJSON(filepath.Join(dir, "system.json"), map[string]any{"system": system}); err != nil {
		return err
	}
	config := []string{"/etc/panasms", "/etc/fstab", "/etc/crypttab", "/etc/mdadm", "/etc/samba", "/etc/exports", "/etc/passwd", "/etc/group", "/etc/shadow", "/etc/gshadow", "/etc/NetworkManager/system-connections", "/var/lib/panasms-modules/registry.json"}
	existing := []string{}
	for _, p := range config {
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := e.capture(ctx, filepath.Join(dir, "configuration.tar"), "tar", append([]string{"--acls", "--xattrs", "--numeric-owner", "-cP", "--"}, existing...)...); err != nil {
		return err
	}
	for _, p := range []string{"/var/lib/panasms/state.db", "/var/lib/panasms-agent/jobs.db"} {
		if _, err := os.Stat(p); err != nil {
			return err
		}
		name := strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", "_")
		if _, err := e.run(ctx, nil, "python3", "-c", sqliteBackup, p, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if err := e.capture(ctx, filepath.Join(dir, "packages.txt"), "dpkg-query", "-W", "-f=${binary:Package}\t${Version}\n"); err != nil {
		return err
	}
	if err := e.capture(ctx, filepath.Join(dir, "mounts.json"), "findmnt", "--json"); err != nil {
		return err
	}
	return e.capture(ctx, filepath.Join(dir, "block-devices.json"), "lsblk", "--json", "-o", "NAME,PATH,UUID,PARTUUID,FSTYPE,MOUNTPOINTS,SIZE")
}
