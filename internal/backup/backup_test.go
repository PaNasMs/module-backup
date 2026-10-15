package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func engine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestBackupRestoreAndRetention(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	e := engine(t)
	e.run = func(ctx context.Context, dest io.Writer, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stderr = os.Stderr
		if dest != nil {
			cmd.Stdout = dest
			return nil, cmd.Run()
		}
		return cmd.Output()
	}
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	if parent := os.Getenv("PANASMS_TEST_REPOSITORY_PARENT"); parent != "" {
		var err error
		repository, err = os.MkdirTemp(parent, "panasms-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(repository) })
	}
	source := filepath.Join(base, "files")
	os.Mkdir(repository, 0700)
	os.Mkdir(source, 0700)
	name := "hello [world]*.txt"
	expected := []byte("version one\n")
	os.WriteFile(filepath.Join(source, name), expected, 0640)
	r, err := e.AddRepository(context.Background(), RepositoryRequest{Name: "Test", Path: repository, Password: "test-encryption-password", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.SavePlan(Plan{Name: "Test", Repository: r.ID, Sources: []Source{{Kind: "files", Path: source}}, KeepLast: 1})
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.Start(JobRequest{Kind: "backup", Plan: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, e, j.ID)
	if j.Status != "succeeded" {
		t.Fatal(j)
	}
	restore, err := e.Start(JobRequest{Kind: "restore", Repository: r.ID, Snapshot: j.Snapshot, Path: filepath.Join(source, name)})
	if err != nil {
		t.Fatal(err)
	}
	restore = waitJob(t, e, restore.ID)
	if restore.Status != "succeeded" {
		t.Fatal(restore)
	}
	got, err := os.ReadFile(filepath.Join(restore.Destination, source, name))
	if err != nil || string(got) != string(expected) {
		t.Fatalf("restore: %q %v", got, err)
	}
	os.WriteFile(filepath.Join(source, name), []byte("version two"), 0640)
	j, err = e.Start(JobRequest{Kind: "backup", Plan: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, e, j.ID)
	if j.Status != "succeeded" {
		t.Fatal(j)
	}
	raw, err := e.query(context.Background(), r.ID, "snapshots")
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []any
	if json.Unmarshal(raw, &snapshots) != nil || len(snapshots) != 1 {
		t.Fatalf("retention: %s", raw)
	}
	check, err := e.Start(JobRequest{Kind: "check", Repository: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, e, check.ID); got.Status != "succeeded" {
		t.Fatal(got)
	}
	imported, err := New(filepath.Join(t.TempDir(), "imported"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = imported.AddRepository(context.Background(), RepositoryRequest{Name: "Imported", Path: repository, Password: "test-encryption-password"})
	if err != nil {
		t.Fatal(err)
	}
}
func waitJob(t *testing.T, e *Engine, id string) Job {
	t.Helper()
	until := time.Now().Add(40 * time.Second)
	for time.Now().Before(until) {
		for _, j := range e.State().Jobs {
			if j.ID == id && j.Status != "running" {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job timed out")
	return Job{}
}
func TestMountLossNeverInvokesRestic(t *testing.T) {
	e := engine(t)
	path := t.TempDir()
	e.mount = func(string) (string, error) { return "replacement", nil }
	e.run = func(context.Context, io.Writer, string, ...string) ([]byte, error) {
		t.Fatal("must not run")
		return nil, nil
	}
	_, err := e.restic(context.Background(), Repository{Path: path, Mount: "original"}, nil, "backup", "x")
	if err == nil {
		t.Fatal("replacement accepted")
	}
}
func TestPlanRejectsOverlapAndSymlinks(t *testing.T) {
	e := engine(t)
	base := t.TempDir()
	source := filepath.Join(base, "source")
	os.Mkdir(source, 0700)
	repo := filepath.Join(source, "repo")
	os.Mkdir(repo, 0700)
	e.state.Repositories = []Repository{{ID: "r", Path: repo}}
	p := Plan{Name: "Test", Repository: "r", KeepLast: 1, Sources: []Source{{Kind: "files", Path: source}}}
	if _, err := e.SavePlan(p); err == nil {
		t.Fatal("overlap accepted")
	}
	link := filepath.Join(base, "link")
	os.Symlink(source, link)
	p.Sources[0].Path = link
	if _, err := e.SavePlan(p); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestRestartMarksInterruptedAndPreservesRecovery(t *testing.T) {
	e := engine(t)
	e.state.Jobs = []Job{{ID: "j", Status: "running"}}
	e.state.Recovery = []string{"container-id"}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	next, err := New(e.root)
	if err != nil {
		t.Fatal(err)
	}
	if next.State().Jobs[0].Status != "interrupted" || len(next.State().Recovery) != 1 {
		t.Fatal(next.State())
	}
}
func TestFailedBackupDoesNotPruneAndRestartsApps(t *testing.T) {
	e := engine(t)
	repo := t.TempDir()
	m, _ := e.mount(repo)
	r := Repository{ID: "r", Path: repo, Mount: m, RepositoryID: strings.Repeat("a", 64)}
	e.state.Repositories = []Repository{r}
	source := t.TempDir()
	sm, _ := e.mount(source)
	p := Plan{ID: "p", Sources: []Source{{Kind: "files", Path: source, Mount: sm}}, KeepLast: 1}
	e.state.Recovery = []string{"was-running"}
	restarted := false
	e.run = func(_ context.Context, _ io.Writer, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "forget") {
			t.Fatal("retention after failure")
		}
		if name == "docker" {
			restarted = true
			return nil, nil
		}
		return nil, errors.New("incomplete backup")
	}
	if err := e.backup(context.Background(), Job{ID: "j"}, p, r); err == nil {
		t.Fatal("incomplete success")
	}
	if !restarted {
		t.Fatal("app not restarted")
	}
}
func TestSQLiteOnlineBackupIncludesWAL(t *testing.T) {
	e := engine(t)
	source := filepath.Join(t.TempDir(), "source.db")
	target := filepath.Join(t.TempDir(), "copy.db")
	script := `import sqlite3,sys,subprocess
c=sqlite3.connect(sys.argv[1]);c.execute('PRAGMA journal_mode=WAL');c.execute('CREATE TABLE test (value TEXT)');c.execute("INSERT INTO test VALUES ('committed-in-wal')");c.commit()
subprocess.run(['python3','-c',sys.argv[3],sys.argv[1],sys.argv[2]],check=True)
assert sqlite3.connect(sys.argv[2]).execute('SELECT value FROM test').fetchone()[0]=='committed-in-wal'
`
	if _, err := e.run(context.Background(), nil, "python3", "-c", script, source, target, sqliteBackup); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRejectsTraversalAndLinks(t *testing.T) {
	for _, name := range []string{"../config", "/etc/shadow", "data/xx/" + strings.Repeat("a", 64), "keys/../../etc/passwd", "locks/" + strings.Repeat("a", 64)} {
		if validRepositoryFile(name) {
			t.Fatal("accepted " + name)
		}
	}
	if !validRepositoryFile("config") || !validRepositoryFile("data/ab/ab"+strings.Repeat("0", 62)) {
		t.Fatal("valid repository path rejected")
	}
}
func TestCloudPathRejectsControlCharacters(t *testing.T) {
	for _, path := range []string{"", "/root", "../outside", "a/../b", "remote:x", "a\nsecret", "."} {
		if cloudPath(path) {
			t.Fatal(path)
		}
	}
	if !cloudPath("PaNasMs/Backups") {
		t.Fatal("valid path rejected")
	}
}
func TestRequestIdentityReconcilesCompletedJobs(t *testing.T) {
	e := engine(t)
	request := JobRequest{Kind: "check", Repository: "r", RequestID: strings.Repeat("a", 32)}
	raw, _ := json.Marshal(request)
	sum := sha256.Sum256(raw)
	e.state.Jobs = []Job{{ID: request.RequestID, Request: hex.EncodeToString(sum[:]), Status: "succeeded"}}
	got, err := e.Start(request)
	if err != nil || got.Status != "succeeded" {
		t.Fatal(got, err)
	}
	request.Kind = "restore"
	if _, err = e.Start(request); err == nil {
		t.Fatal("reused key accepted")
	}
}

func TestWorkspacePinsStorageAndRejectsSourceOverlap(t *testing.T) {
	e := engine(t)
	parent := t.TempDir()
	e.mount = func(string) (string, error) { return "disk-original", nil }
	workspace, err := e.SetWorkspace(parent)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Path != filepath.Join(parent, "panasms-backup-work") {
		t.Fatal(workspace)
	}
	e.mount = func(string) (string, error) { return "different-disk", nil }
	if err := e.checkWorkspace(); err == nil {
		t.Fatal("disconnected disk accepted")
	}
	e.state.Plans = []Plan{{Sources: []Source{{Kind: "files", Path: parent}}}}
	if _, err := e.SetWorkspace(parent); err == nil {
		t.Fatal("recursive backup accepted")
	}
}

func TestRepositoryReconnectPreservesIdentityAndPassword(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	e := engine(t)
	dir := t.TempDir()
	original, err := e.AddRepository(context.Background(), RepositoryRequest{Name: "Original", Path: dir, Provider: "local", Password: "original-password", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.AddRepository(context.Background(), RepositoryRequest{ID: original.ID, Name: "Changed", Path: dir, Provider: "local", Password: "wrong-password"})
	if err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err = e.query(context.Background(), original.ID, "snapshots"); err != nil {
		t.Fatalf("failed reconnect damaged original key: %v", err)
	}
	updated, err := e.AddRepository(context.Background(), RepositoryRequest{ID: original.ID, Name: "Changed", Path: dir, Provider: "local", Password: "original-password"})
	if err != nil || updated.ID != original.ID || len(e.State().Repositories) != 1 {
		t.Fatalf("%+v %v", updated, err)
	}
}
