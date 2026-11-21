package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/backup"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/urfave/cli/v3"
)

// backupSecret is the value the tests give as the bucket's secret key; no
// output and no error may hold it.
const backupSecret = "bucket-secret-value-77"

// bucketServer is an S3-compatible endpoint that keeps the objects put in
// it, or refuses with a status.
type bucketServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	refuse  int
}

func newBucketServer(t *testing.T) *bucketServer {
	t.Helper()
	b := &bucketServer{objects: map[string][]byte{}}
	b.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.refuse != 0 {
			http.Error(w, "<Error><Code>AccessDenied</Code><Message>"+backupSecret+"</Message></Error>", b.refuse)
			return
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		b.objects[strings.TrimPrefix(r.URL.Path, "/")] = buf.Bytes()
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *bucketServer) object(t *testing.T) []byte {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.objects) != 1 {
		t.Fatalf("the bucket holds %d objects", len(b.objects))
	}
	for _, v := range b.objects {
		return v
	}
	return nil
}

// backupDir is a node's data directory with a database, a workspace, a
// master key and a settings file that names the bucket.
func backupDir(t *testing.T, b *bucketServer) (dir, db string) {
	t.Helper()
	clearEnv(t)
	dir = t.TempDir()
	db = filepath.Join(dir, "ganglion.db")
	conn, err := sqlitedb.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("CREATE TABLE memo (id INTEGER PRIMARY KEY, topic TEXT); INSERT INTO memo (topic) VALUES ('birthdays')"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspace", "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace", "memory", "birthdays.md"), []byte("# birthdays"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := vault.LoadKey(secrets.None{}, vault.KeyPath(db)); err != nil {
		t.Fatal(err)
	}
	yaml := "backup:\n  endpoint: " + b.srv.URL + "\n  region: us-east-1\n  bucket: bkt\n  prefix: node\n" +
		"  access_key_id: EXAMPLEKEYID\n  secret_access_key: env:BACKUP_SECRET\nagent:\n  workspace: " + filepath.Join(dir, "workspace") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ganglion.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BACKUP_SECRET", backupSecret)
	return dir, db
}

// backupRun runs `ganglion backup <args>`, trusting the test bucket, and
// fails the test when the secret shows anywhere.
func backupRun(t *testing.T, b *bucketServer, args ...string) (string, error) {
	t.Helper()
	return backupRunAfter(t, b, time.After, args...)
}

// backupRunAfter is backupRun with the wait for a running node's answer.
func backupRunAfter(t *testing.T, b *bucketServer, after backup.AfterFunc, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := &cli.Command{Name: "ganglion", Writer: &out, ErrWriter: &errOut,
		Commands: []*cli.Command{backupCommand(&out, b.srv.Client(), after)}}
	err := root.Run(context.Background(), append([]string{"ganglion", "backup"}, args...))
	shown := out.String() + errOut.String()
	if err != nil {
		shown += err.Error()
	}
	if strings.Contains(shown, backupSecret) {
		t.Fatalf("backup %v shows the secret:\n%s", args, shown)
	}
	return out.String(), err
}

func TestBackupNowPutsOneSealedObjectAndRestoreOpensIt(t *testing.T) {
	b := newBucketServer(t)
	dir, db := backupDir(t, b)
	out, err := backupRun(t, b, "now", "--db", db)
	if err != nil || !strings.HasPrefix(out, "backup made: node/ganglion-") || !strings.Contains(out, ".tar.gz.enc, ") {
		t.Fatalf("now: %q, %v", out, err)
	}
	object := b.object(t)
	if bytes.Contains(object, []byte("birthdays")) {
		t.Fatal("the object is not sealed")
	}
	if last, err := backup.Last(dir); err != nil || !last.OK || last.Size != int64(len(object)) {
		t.Fatalf("the recorded result: %+v, %v", last, err)
	}
	archive := filepath.Join(t.TempDir(), "node.tar.gz.enc")
	if err := os.WriteFile(archive, object, 0o600); err != nil {
		t.Fatal(err)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	keyFile := vault.KeyPath(db)
	out, err = backupRun(t, b, "restore", "--data", restored, "--key-file", keyFile, archive)
	if err != nil || !strings.Contains(out, "restored ") || !strings.Contains(out, filepath.Join(restored, "ganglion.db")) {
		t.Fatalf("restore: %q, %v", out, err)
	}
	if got, err := os.ReadFile(filepath.Join(restored, "workspace", "memory", "birthdays.md")); err != nil || string(got) != "# birthdays" {
		t.Fatalf("the workspace file: %q, %v", got, err)
	}
	// The cells export is left for the node's first start, not in the bus's store.
	if info, err := os.Stat(backup.ImportPath(restored)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the cells export: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(restored, "bus")); err == nil {
		t.Fatal("restore wrote the bus's store folder")
	}
	if _, err := os.Stat(filepath.Join(restored, "master.key")); err == nil {
		t.Fatal("restore wrote a master key")
	}
	conn, err := sqlitedb.Open(context.Background(), filepath.Join(restored, "ganglion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var topic string
	if err := conn.QueryRow("SELECT topic FROM memo").Scan(&topic); err != nil || topic != "birthdays" {
		t.Fatalf("the restored row: %q, %v", topic, err)
	}
}

func TestBackupRestoreTakesTheKeyFromTheEnvironmentToo(t *testing.T) {
	b := newBucketServer(t)
	_, db := backupDir(t, b)
	if _, err := backupRun(t, b, "now", "--db", db); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "a.enc")
	if err := os.WriteFile(archive, b.object(t), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(vault.KeyPath(db))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(vault.KeyEnv, base64.StdEncoding.EncodeToString(raw))
	dst := filepath.Join(t.TempDir(), "r")
	if _, err := backupRun(t, b, "restore", "--data", dst, archive); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "ganglion.db")); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreRefusesWithoutWritingOrMakingAKey(t *testing.T) {
	b := newBucketServer(t)
	_, db := backupDir(t, b)
	if _, err := backupRun(t, b, "now", "--db", db); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "a.enc")
	if err := os.WriteFile(archive, b.object(t), 0o600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	wrong := filepath.Join(base, "wrong.key")
	if _, _, err := vault.LoadKey(secrets.None{}, wrong); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no key at all":    {[]string{"--data", filepath.Join(base, "a"), archive}, "no master key"},
		"a missing file":   {[]string{"--data", filepath.Join(base, "b"), "--key-file", filepath.Join(base, "none.key"), archive}, "master key"},
		"the wrong key":    {[]string{"--data", filepath.Join(base, "c"), "--key-file", wrong, archive}, "nothing was restored"},
		"no archive":       {[]string{"--data", filepath.Join(base, "d")}, "usage"},
		"a missing --data": {[]string{archive}, "data"},
	} {
		_, err := backupRun(t, b, append([]string{"restore"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one holding %q", name, err, tc.want)
		}
	}
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if e.Name() != "wrong.key" {
			t.Errorf("%s was made", e.Name())
		}
	}
	// A directory that is not empty is refused.
	full := filepath.Join(base, "full")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backupRun(t, b, "restore", "--data", full, "--key-file", vault.KeyPath(db), archive); err == nil {
		t.Fatal("restore wrote into a directory that is not empty")
	}
}

func TestBackupNowRefusals(t *testing.T) {
	b := newBucketServer(t)
	dir, db := backupDir(t, b)

	t.Run("the bucket refuses", func(t *testing.T) {
		b.mu.Lock()
		b.refuse = http.StatusForbidden
		b.mu.Unlock()
		_, err := backupRun(t, b, "now", "--db", db)
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "AccessDenied") {
			t.Fatalf("error %v", err)
		}
		if last, lerr := backup.Last(dir); lerr != nil || last.OK || last.Kind != backup.FailureRefused {
			t.Fatalf("the recorded result: %+v, %v", last, lerr)
		}
	})
	t.Run("no section", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "ganglion.yaml"), []byte("node:\n  tz: UTC\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := backupRun(t, b, "now", "--db", db); err == nil || !strings.Contains(err.Error(), "backups are off") {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("no database", func(t *testing.T) {
		if _, err := backupRun(t, b, "now", "--db", filepath.Join(dir, "none.db")); err == nil || !strings.Contains(err.Error(), "no database") {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("an argument", func(t *testing.T) {
		if _, err := backupRun(t, b, "now", "--db", db, "extra"); err == nil {
			t.Fatal("an argument was accepted")
		}
	})
}

func TestBackupNowNeverMakesAMasterKey(t *testing.T) {
	b := newBucketServer(t)
	dir, db := backupDir(t, b)
	if err := os.Remove(vault.KeyPath(db)); err != nil {
		t.Fatal(err)
	}
	_, err := backupRun(t, b, "now", "--db", db)
	if err == nil || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("error %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, vault.KeyFile)); serr == nil {
		t.Fatal("a master key was made")
	}
}

func TestBackupNowNeedsTheSecretKey(t *testing.T) {
	b := newBucketServer(t)
	_, db := backupDir(t, b)
	t.Setenv("BACKUP_SECRET", "")
	_, err := backupRun(t, b, "now", "--db", db)
	if err == nil || !strings.Contains(err.Error(), "backup.secret_access_key") || !strings.Contains(err.Error(), "BACKUP_SECRET") {
		t.Fatalf("error %v", err)
	}
}

// answerer plays a running node for `backup now`: at its first look for the
// answer it records a result carrying the id of the request file.
type answerer struct {
	t     *testing.T
	dir   string
	key   string
	size  int64
	polls int
}

func (a *answerer) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	if d != backup.AnswerPoll {
		return ch
	}
	a.polls++
	id, err := os.ReadFile(backup.RequestPath(a.dir))
	if err != nil {
		a.t.Fatalf("no request: %v", err)
	}
	data, _ := json.Marshal(backup.Result{OK: true, Key: a.key, Size: a.size, Request: string(id)})
	if err := os.WriteFile(backup.ResultPath(a.dir), data, 0o600); err != nil {
		a.t.Fatal(err)
	}
	ch <- time.Time{}
	return ch
}

// While a node holds the database lock the command opens neither the
// database nor the bus's store: it asks the node through the request file
// and prints what the node recorded.
func TestBackupNowAsksARunningNode(t *testing.T) {
	b := newBucketServer(t)
	dir, db := backupDir(t, b)
	unlock, err := node.LockDB(db)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	a := &answerer{t: t, dir: dir, key: "node/ganglion-x.tar.gz.enc", size: 321}
	out, err := backupRunAfter(t, b, a.after, "now", "--db", db)
	if err != nil || out != "backup made: node/ganglion-x.tar.gz.enc, 321 bytes\n" || a.polls != 1 {
		t.Fatalf("now: %q, %v after %d looks", out, err, a.polls)
	}
	if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
		t.Fatal("the command opened the bus's store beside a running node")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.objects) != 0 {
		t.Fatalf("the command put %d objects itself", len(b.objects))
	}
}

func TestBackupNowSaysWhenTheNodeDoesNotAnswer(t *testing.T) {
	b := newBucketServer(t)
	dir, db := backupDir(t, b)
	unlock, err := node.LockDB(db)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	late := func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		if d == backup.AnswerWait {
			ch <- time.Time{}
		}
		return ch
	}
	_, err = backupRunAfter(t, b, late, "now", "--db", db)
	if err == nil || !errors.Is(err, backup.ErrNoAnswer) || !strings.Contains(err.Error(), "is the node running") {
		t.Fatalf("error %v", err)
	}
	if _, serr := os.Stat(backup.RequestPath(dir)); serr == nil {
		t.Fatal("the request is left")
	}
}

// A node that records a failure is reported as a failure.
func TestBackupNowReportsTheFailureANodeRecorded(t *testing.T) {
	b := newBucketServer(t)
	dir, db := backupDir(t, b)
	unlock, err := node.LockDB(db)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fail := func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		if d == backup.AnswerPoll {
			id, _ := os.ReadFile(backup.RequestPath(dir))
			data, _ := json.Marshal(backup.Result{Reason: "backup: put: status 403", Request: string(id)})
			_ = os.WriteFile(backup.ResultPath(dir), data, 0o600)
			ch <- time.Time{}
		}
		return ch
	}
	_, err = backupRunAfter(t, b, fail, "now", "--db", db)
	if err == nil || !strings.Contains(err.Error(), "the backup failed: backup: put: status 403") {
		t.Fatalf("error %v", err)
	}
}

// With no node running the command makes the backup itself, and the lock is
// released afterwards.
func TestBackupNowReleasesTheLock(t *testing.T) {
	b := newBucketServer(t)
	_, db := backupDir(t, b)
	if _, err := backupRun(t, b, "now", "--db", db); err != nil {
		t.Fatal(err)
	}
	unlock, err := node.LockDB(db)
	if err != nil {
		t.Fatalf("the lock is still held: %v", err)
	}
	unlock()
}
