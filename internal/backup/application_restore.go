package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func dockerAPI(ctx context.Context, route string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/v1.41/"+route, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Docker is unavailable")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("Docker could not recreate the application; restored files have been retained")
	}
	return body, nil
}
func recoveryMetadata(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && d.Name() == "recovery.json" && filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path)))) == "staging" {
			if found != "" {
				return errors.New("Select a backup containing one recovery manifest")
			}
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("Recovery manifest not found")
	}
	return found, nil
}
func (e *Engine) restoreApplications(ctx context.Context, j Job) error {
	manifest, err := recoveryMetadata(j.Destination)
	if err != nil {
		return err
	}
	stage := filepath.Dir(manifest)
	raw, err := os.ReadFile(filepath.Join(stage, "containers.json"))
	if err != nil {
		return err
	}
	var containers []struct {
		ID     string         `json:"Id"`
		Name   string         `json:"Name"`
		Config map[string]any `json:"Config"`
		Mounts []struct {
			Type        string `json:"Type"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
	}
	if json.Unmarshal(raw, &containers) != nil || len(containers) == 0 {
		return errors.New("No applications in this backup")
	}
	volumesRaw, err := os.ReadFile(filepath.Join(stage, "volumes.json"))
	if err != nil {
		return err
	}
	volumeCopies := map[string]string{}
	if err = json.Unmarshal(volumesRaw, &volumeCopies); err != nil {
		return err
	}
	created := []map[string]string{}
	for _, c := range containers {
		if !snapshotID(c.ID) || !validContainer(strings.TrimPrefix(c.Name, "/")) {
			return errors.New("Invalid application manifest")
		}
		rootfs := filepath.Join(stage, "rootfs-"+c.ID+".tar")
		if err = cleanPath(rootfs); err != nil {
			return err
		}
		image, err := e.run(ctx, nil, "docker", "image", "import", rootfs)
		if err != nil {
			return err
		}
		imageID := strings.TrimSpace(string(image))
		if !strings.HasPrefix(imageID, "sha256:") {
			return errors.New("Image import returned no identity")
		}
		binds := []map[string]any{}
		for _, m := range c.Mounts {
			if !filepath.IsAbs(m.Source) || filepath.Clean(m.Source) != m.Source {
				return errors.New("Invalid application data path")
			}
			relative := volumeCopies[m.Source]
			if !strings.HasPrefix(relative, "volumes/") || filepath.Clean(relative) != relative || strings.Contains(relative, "..") {
				return errors.New("Invalid captured application volume")
			}
			source := filepath.Join(stage, relative)
			if err = cleanPath(source); err != nil {
				return err
			}
			binds = append(binds, map[string]any{"Type": "bind", "Source": source, "Target": m.Destination, "ReadOnly": !m.RW})
		}
		config := c.Config
		config["Image"] = imageID
		config["HostConfig"] = map[string]any{"NetworkMode": "none", "RestartPolicy": map[string]string{"Name": "no"}, "Mounts": binds}
		delete(config, "NetworkingConfig")
		name := "restored-" + strings.TrimPrefix(c.Name, "/") + "-" + j.ID[:8]
		raw, err = dockerAPI(ctx, "containers/create?name="+url.QueryEscape(name), config)
		if err != nil {
			return err
		}
		var result struct {
			ID string `json:"Id"`
		}
		if json.Unmarshal(raw, &result) != nil || result.ID == "" {
			return errors.New("Docker returned no container identity")
		}
		created = append(created, map[string]string{"id": result.ID, "name": name, "image": imageID})
		if err = atomicJSON(filepath.Join(j.Destination, "restored-applications.json"), created); err != nil {
			return err
		}
	}
	return nil
}
