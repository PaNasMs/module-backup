package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const githubLimit = 32 << 20

var githubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type releaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type backupRelease struct {
	Body    string         `json:"body"`
	ID      int64          `json:"id"`
	Tag     string         `json:"tag_name"`
	Created string         `json:"created_at"`
	Assets  []releaseAsset `json:"assets"`
}
type githubSnapshot struct {
	ID    string   `json:"id"`
	Time  string   `json:"time"`
	Paths []string `json:"paths"`
	Tags  []string `json:"tags"`
}

func githubCall(ctx context.Context, r Repository, method, endpoint string, body []byte, binary bool) ([]byte, error) {
	access, err := token(ctx, r)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if binary {
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > 4 || next.URL.Scheme != "https" {
			return errors.New("Invalid GitHub asset redirect")
		}
		next.Header.Del("Authorization")
		return nil
	}}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub is unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub request failed (HTTP %d); check repository access and quota", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, githubLimit+1))
	if len(raw) > githubLimit {
		return nil, errors.New("GitHub configuration archive exceeds 32 MiB")
	}
	return raw, err
}
func githubAPI(r Repository, resource string) string {
	return "https://api.github.com/repos/" + r.Path + "/" + resource
}
func verifyGithub(ctx context.Context, r Repository) (string, error) {
	if !githubName.MatchString(r.Path) {
		return "", errors.New("Choose an existing private GitHub repository as owner/name")
	}
	raw, err := githubCall(ctx, r, "GET", strings.TrimSuffix(githubAPI(r, ""), "/"), nil, false)
	if err != nil {
		return "", err
	}
	var repo struct {
		ID          int64 `json:"id"`
		Private     bool  `json:"private"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if json.Unmarshal(raw, &repo) != nil || repo.ID <= 0 || !repo.Private || !repo.Permissions.Push {
		return "", errors.New("Choose a private GitHub repository with write access")
	}
	id := strconv.FormatInt(repo.ID, 10)
	if r.RepositoryID != "" && r.RepositoryID != id {
		return "", errors.New("GitHub repository identity changed")
	}
	return id, nil
}
func githubReleases(ctx context.Context, r Repository) ([]backupRelease, error) {
	if _, err := verifyGithub(ctx, r); err != nil {
		return nil, err
	}
	result := []backupRelease{}
	for page := 1; page <= 10; page++ {
		raw, err := githubCall(ctx, r, "GET", githubAPI(r, fmt.Sprintf("releases?per_page=100&page=%d", page)), nil, false)
		if err != nil {
			return nil, err
		}
		var list []backupRelease
		if err = json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		for _, item := range list {
			if snapshotID(strings.TrimPrefix(item.Tag, "panasms-backup-")) && strings.HasPrefix(item.Tag, "panasms-backup-") {
				complete, data := false, false
				for _, a := range item.Assets {
					complete = complete || a.Name == "complete.json"
					data = data || a.Name == "backup.tar"
				}
				if complete && data {
					result = append(result, item)
				}
			}
		}
		if len(list) < 100 {
			sort.Slice(result, func(i, j int) bool { return releaseTime(result[i]).After(releaseTime(result[j])) })
			return result, nil
		}
	}
	return nil, errors.New("GitHub repository has too many releases; use a dedicated backup repository")
}
func (e *Engine) githubSnapshots(ctx context.Context, r Repository) ([]byte, error) {
	releases, err := githubReleases(ctx, r)
	if err != nil {
		return nil, err
	}
	list := []githubSnapshot{}
	for _, release := range releases {
		list = append(list, githubSnapshot{ID: strings.TrimPrefix(release.Tag, "panasms-backup-"), Time: releaseTime(release).Format(time.RFC3339), Paths: []string{"Platform configuration"}, Tags: []string{}})
	}
	return json.Marshal(list)
}
func (e *Engine) githubLocal(r Repository, suffix string) (Repository, error) {
	r.Local = filepath.Join(e.root, "github", r.ID, suffix)
	if err := os.MkdirAll(r.Local, 0700); err != nil {
		return r, err
	}
	return r, nil
}
func (e *Engine) downloadGithub(ctx context.Context, r Repository, snapshot string) (Repository, error) {
	releases, err := githubReleases(ctx, r)
	if err != nil {
		return r, err
	}
	var selected *backupRelease
	for i, item := range releases {
		if snapshot == "" || item.Tag == "panasms-backup-"+snapshot {
			selected = &releases[i]
			break
		}
	}
	if selected == nil {
		return r, errors.New("Backup version not found")
	}
	var data, manifest []byte
	for _, asset := range selected.Assets {
		if asset.Name != "backup.tar" && asset.Name != "complete.json" {
			continue
		}
		if asset.Size > githubLimit {
			return r, errors.New("GitHub configuration archive exceeds 32 MiB")
		}
		raw, err := githubCall(ctx, r, "GET", githubAPI(r, "releases/assets/"+strconv.FormatInt(asset.ID, 10)), nil, true)
		if err != nil {
			return r, err
		}
		if asset.Name == "backup.tar" {
			data = raw
		} else {
			manifest = raw
		}
	}
	var receipt struct {
		SHA256   string `json:"sha256"`
		Snapshot string `json:"snapshot"`
	}
	sum := sha256.Sum256(data)
	if json.Unmarshal(manifest, &receipt) != nil || receipt.SHA256 != hex.EncodeToString(sum[:]) || "panasms-backup-"+receipt.Snapshot != selected.Tag {
		return r, errors.New("GitHub archive checksum or version identity does not match")
	}
	r, err = e.githubLocal(r, newID())
	if err != nil {
		return r, err
	}
	if err = unpackRepository(r.Local, data); err != nil {
		return r, err
	}
	return r, nil
}
func unpackRepository(root string, data []byte) error {
	reader := tar.NewReader(bytes.NewReader(data))
	total := int64(0)
	for {
		h, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := h.Name
		if !validRepositoryFile(name) || h.Typeflag != tar.TypeReg || h.Size < 0 {
			return errors.New("Invalid repository archive entry")
		}
		total += h.Size
		if total > githubLimit {
			return errors.New("Configuration archive exceeds limit")
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.CopyN(file, reader, h.Size)
		ce := file.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
	}
}
func validRepositoryFile(name string) bool {
	if name == "config" {
		return true
	}
	parts := strings.Split(name, "/")
	if len(parts) == 2 && (parts[0] == "keys" || parts[0] == "snapshots" || parts[0] == "index") {
		return snapshotID(parts[1])
	}
	return len(parts) == 3 && parts[0] == "data" && len(parts[1]) == 2 && snapshotID(parts[2]) && strings.HasPrefix(parts[2], parts[1])
}
func packRepository(root string) ([]byte, error) {
	out := &boundedBuffer{}
	writer := tar.NewWriter(out)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if !validRepositoryFile(name) {
			return errors.New("Unexpected repository content")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > githubLimit {
			return errors.New("Configuration archive exceeds limits")
		}
		if err = writer.WriteHeader(&tar.Header{Name: name, Size: info.Size(), Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(writer, f)
		return err
	})
	if err == nil {
		err = writer.Close()
	}
	return out.Bytes(), err
}
func (e *Engine) uploadGithub(ctx context.Context, r Repository, j Job, p Plan) error {
	data, err := packRepository(r.Local)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	receipt, _ := json.Marshal(map[string]any{"snapshot": j.Snapshot, "sha256": hex.EncodeToString(sum[:]), "format": 1, "plan": p.ID})
	body, _ := json.Marshal(map[string]any{"tag_name": "panasms-backup-" + j.Snapshot, "name": "PaNasMs configuration " + j.Started.Format(time.RFC3339), "body": string(mustJSON(map[string]string{"format": "panasms-backup-1", "plan": p.ID, "created": j.Started.Format(time.RFC3339)})), "draft": true})
	raw, err := githubCall(ctx, r, "POST", githubAPI(r, "releases"), body, false)
	if err != nil {
		return err
	}
	var created backupRelease
	if json.Unmarshal(raw, &created) != nil || created.ID == 0 {
		return errors.New("Invalid GitHub release response")
	}
	for _, asset := range []struct {
		name string
		data []byte
	}{{"backup.tar", data}, {"complete.json", receipt}} {
		endpoint := "https://uploads.github.com/repos/" + r.Path + "/releases/" + strconv.FormatInt(created.ID, 10) + "/assets?name=" + asset.name
		if _, err = githubCall(ctx, r, "POST", endpoint, asset.data, true); err != nil {
			return err
		}
	}
	return nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func releaseMetadata(r backupRelease) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(r.Body), &m)
	return m
}
func releaseTime(r backupRelease) time.Time {
	date, _ := time.Parse(time.RFC3339, releaseMetadata(r)["created"])
	return date
}
func (e *Engine) githubRetention(ctx context.Context, r Repository, p Plan) error {
	releases, err := githubReleases(ctx, r)
	if err != nil {
		return err
	}
	daily, weekly := map[string]bool{}, map[string]bool{}
	count := 0
	for _, item := range releases {
		meta := releaseMetadata(item)
		if meta["format"] != "panasms-backup-1" || meta["plan"] != p.ID || releaseTime(item).IsZero() {
			continue
		}
		count++
		keep := count <= p.KeepLast
		date := releaseTime(item)
		day := date.UTC().Format("2006-01-02")
		year, week := date.UTC().ISOWeek()
		weekKey := fmt.Sprintf("%d-%d", year, week)
		if !daily[day] && len(daily) < p.KeepDaily {
			daily[day] = true
			keep = true
		}
		if !weekly[weekKey] && len(weekly) < p.KeepWeekly {
			weekly[weekKey] = true
			keep = true
		}
		if !keep {
			if _, err = githubCall(ctx, r, "DELETE", githubAPI(r, "releases/"+strconv.FormatInt(item.ID, 10)), nil, false); err != nil {
				return err
			}
		}
	}
	return nil
}
