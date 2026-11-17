package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeUploader keeps what it is given.
type fakeUploader struct {
	keys []string
	body []byte
	err  error
	// sawWork is what the work folder held during the upload.
	sawWork os.FileMode
	dir     string
}

func (u *fakeUploader) Put(_ context.Context, key string, body io.ReadSeeker, size int64) error {
	if u.err != nil {
		return u.err
	}
	data, err := io.ReadAll(body)
	if err != nil || int64(len(data)) != size {
		return errors.New("size differs")
	}
	if st, err := os.Stat(filepath.Join(u.dir, workDirName)); err == nil {
		u.sawWork = st.Mode().Perm()
	}
	u.keys, u.body = append(u.keys, key), data
	return nil
}

func newBackup(t *testing.T, n *node, up Uploader, prefix string) *Backup {
	t.Helper()
	cfg := DefaultConfig(n.dir)
	cfg.Prefix, cfg.Version, cfg.Schema = prefix, "v9", 7
	return New(n.db, up, testKey(3), cfg, WithBackupClock(func() time.Time { return testTime }))
}

func TestRunBacksUpAndRestoreReadsItBack(t *testing.T) {
	n := newNode(t)
	up := &fakeUploader{dir: n.dir}
	res, err := newBackup(t, n, up, "/family/node/").Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "family/node/ganglion-20261117T030000Z.tar.gz.enc"; len(up.keys) != 1 || up.keys[0] != want || res.Key != want {
		t.Fatalf("keys %v, result %+v", up.keys, res)
	}
	if !res.OK || res.Size != int64(len(up.body)) || !res.Time.Equal(testTime) || !res.LastOK.Equal(testTime) {
		t.Fatalf("result: %+v", res)
	}
	if up.sawWork != 0o700 {
		t.Errorf("work folder mode %v during the upload", up.sawWork)
	}
	if _, err := os.Stat(filepath.Join(n.dir, workDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the work folder is left: %v", err)
	}
	last, err := Last(n.dir)
	if err != nil || last != res {
		t.Fatalf("Last = %+v, %v; want %+v", last, err, res)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	m, err := Restore(context.Background(), bytes.NewReader(up.body), testKey(3), dst)
	if err != nil || m.Version != "v9" || m.Schema != 7 {
		t.Fatalf("restore: %+v %v", m, err)
	}
	if got := readFile(t, filepath.Join(dst, "workspace", "notes", "a.md")); got != "# a" {
		t.Fatal(got)
	}
}

func TestRunWithoutAPrefixHasNoLeadingSlash(t *testing.T) {
	n := newNode(t)
	up := &fakeUploader{dir: n.dir}
	if _, err := newBackup(t, n, up, "").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if up.keys[0] != "ganglion-20261117T030000Z.tar.gz.enc" {
		t.Fatalf("key %q", up.keys[0])
	}
}

func TestRunRecordsAFailureAndKeepsTheLastSuccess(t *testing.T) {
	n := newNode(t)
	ok := newBackup(t, n, &fakeUploader{dir: n.dir}, "p")
	if _, err := ok.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	later := testTime.Add(24 * time.Hour)
	cfg := DefaultConfig(n.dir)
	bad := New(n.db, &fakeUploader{err: &StatusError{Status: 403, Code: "AccessDenied"}}, testKey(3), cfg,
		WithBackupClock(func() time.Time { return later }))
	res, err := bad.Run(context.Background())
	if _, ok := errors.AsType[*StatusError](err); !ok {
		t.Fatalf("err = %v", err)
	}
	if res.OK || res.Reason != "backup: put: status 403: AccessDenied" || res.Key != "" || !res.Time.Equal(later) || !res.LastOK.Equal(testTime) {
		t.Fatalf("result: %+v", res)
	}
	last, err := Last(n.dir)
	if err != nil || last != res {
		t.Fatalf("Last = %+v, %v", last, err)
	}
	if _, err := os.Stat(filepath.Join(n.dir, workDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the work folder is left after a failure: %v", err)
	}
	if list, _ := filepath.Glob(filepath.Join(n.dir, ResultFile+".*")); len(list) != 0 {
		t.Errorf("temp files left: %v", list)
	}
}

func TestRunClearsAStaleWorkFolderAndCleansUpAfterAFailedStep(t *testing.T) {
	// A stale folder from a crash goes, and so does the one a failed step
	// leaves. A closed database fails the first step.
	n := newNode(t)
	writeFile(t, filepath.Join(n.dir, workDirName, "ganglion.db"), "left by a crash", 0o600)
	if _, err := newBackup(t, n, &fakeUploader{dir: n.dir}, "").Run(context.Background()); err != nil {
		t.Fatalf("a stale work folder broke the backup: %v", err)
	}
	closed := newNode(t)
	_ = closed.db.Close()
	b := newBackup(t, closed, &fakeUploader{dir: closed.dir}, "")
	writeFile(t, filepath.Join(closed.dir, workDirName, "stale"), "x", 0o600)
	if _, err := b.Run(context.Background()); err == nil {
		t.Fatal("a closed database was backed up")
	}
	if _, err := os.Stat(filepath.Join(closed.dir, workDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("work folder: %v", err)
	}
	last, err := Last(closed.dir)
	if err != nil || last.OK || last.Reason == "" {
		t.Fatalf("Last = %+v, %v", last, err)
	}
}

func TestLastWithoutABackup(t *testing.T) {
	if _, err := Last(t.TempDir()); !errors.Is(err, ErrNoResult) {
		t.Fatalf("err = %v", err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ResultFile), "{broken", 0o600)
	if _, err := Last(dir); err == nil || errors.Is(err, ErrNoResult) {
		t.Fatalf("err = %v", err)
	}
}

func TestResultFileIsPrivate(t *testing.T) {
	n := newNode(t)
	if _, err := newBackup(t, n, &fakeUploader{dir: n.dir}, "").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(n.dir, ResultFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
}

func TestRunEndToEndThroughTheS3Client(t *testing.T) {
	n := newNode(t)
	f := newFakeS3(t)
	b := New(n.db, f.client(t), testKey(3), DefaultConfig(n.dir), WithBackupClock(func() time.Time { return testTime }))
	if _, err := b.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	obj := f.objects["/bkt/ganglion-20261117T030000Z.tar.gz.enc"]
	if len(obj) == 0 {
		t.Fatalf("objects: %v", keys(f.objects))
	}
	if _, err := Restore(context.Background(), bytes.NewReader(obj), testKey(3), filepath.Join(t.TempDir(), "d")); err != nil {
		t.Fatal(err)
	}
}

// A backup refused because another holds the lock records nothing.
func TestRunRefusedWhileAnotherRuns(t *testing.T) {
	dir := t.TempDir()
	release, err := lockRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	b := New(nil, nil, make([]byte, KeySize), DefaultConfig(dir))
	if _, err := b.Run(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("Run = %v, want ErrBusy", err)
	}
	if _, err := Last(dir); !errors.Is(err, ErrNoResult) {
		t.Fatalf("Last = %v, want ErrNoResult", err)
	}
	if _, err := os.Stat(filepath.Join(dir, workDirName)); !os.IsNotExist(err) {
		t.Fatalf("work folder: %v, want none", err)
	}
}
