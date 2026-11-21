package config

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the test checks the Content-MD5 header the bucket is sent
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/backup"
	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/guard"
	"github.com/igorrius/ganglion/internal/testinputs"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner/runnertest"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/samber/do/v2"
)

// fakeBucket is an S3-compatible endpoint that keeps what is put in it and
// checks that each request is signed and carries its MD5.
type fakeBucket struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeBucket(t *testing.T) (*fakeBucket, *httptest.Server) {
	t.Helper()
	f := &fakeBucket{t: t, objects: map[string][]byte{}}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	sum := md5.Sum(body) //nolint:gosec // see the import
	switch {
	case err != nil, r.Method != http.MethodPut,
		!strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=EXAMPLEKEYID/"),
		r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]):
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	f.objects[strings.TrimPrefix(r.URL.Path, "/")] = body
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *fakeBucket) only() (string, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.objects) != 1 {
		f.t.Fatalf("the bucket holds %d objects", len(f.objects))
	}
	for k, v := range f.objects {
		return k, v
	}
	return "", nil
}

// factGuard lets every fact through.
type factGuard struct{}

func (factGuard) CheckFact(context.Context, string) (guard.Verdict, error) {
	return guard.Verdict{OK: true, Reason: "a fact"}, nil
}

func (factGuard) CheckPersona(context.Context, string) (guard.Verdict, error) {
	return guard.Verdict{OK: true, Reason: "a persona"}, nil
}

const backupAcceptManifest = `name: probe
version: 1
triggers:
  - tool: token
    about: A module that is only installed.
    output: {value: string, ok: boolean}
`

// backupNode is a node whose data directory is dir.
func backupNode(t *testing.T, dir string, endpoint string, hc *http.Client) Bootstrap {
	t.Helper()
	b := testConfig(t)
	b.DB, b.ModulesDir, b.MasterKey = filepath.Join(dir, "ganglion.db"), filepath.Join(dir, "modules"), filepath.Join(dir, "master.key")
	b.SettingsPath, b.BusDir = filepath.Join(dir, "ganglion.yaml"), bus.StorePath(dir)
	b.Agent = AgentSettings{TZ: time.UTC, Workspace: filepath.Join(dir, "workspace")}
	b.Settings = settings.Default()
	b.Settings.Backup = &settings.Backup{Endpoint: endpoint, Region: "us-east-1", Bucket: "bucket", Prefix: "node",
		AccessKeyID: "EXAMPLEKEYID", SecretAccessKey: "env:BACKUP_SECRET", At: "03:00"}
	b.BackupSecret, b.BackupHTTP = "backup-secret-value-0001", hc
	return b
}

// R-BAK-1 – R-BAK-6, ADR-0095: a node's data is backed up to an
// S3-compatible bucket and restored into a new directory, and a node started
// on it finds its memory topic, its installed module and its cell.
func TestBackupAcceptance(t *testing.T) {
	testinputs.Build(t, "../runner/runnertest/probe/wasm", "GOOS=wasip1", "GOARCH=wasm")
	wasm, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bucket, srv := newFakeBucket(t)
	dir := t.TempDir()
	b := backupNode(t, dir, srv.URL, srv.Client())
	if err := os.WriteFile(b.SettingsPath, []byte("node:\n  tz: UTC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	i := New(ctx, b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })

	// What the node holds: a memory topic, an installed module, a cell.
	repo := do.MustInvoke[*gitrepo.Repo](i)
	if _, err := memory.New(repo, factGuard{}, time.Now).Apply(ctx, []memory.Op{{Kind: memory.OpAdd, Topic: "birthdays",
		Summary: "Birthdays", Text: "Anna: 12 May"}}, memory.Source{Turn: "t1"}); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(backupAcceptManifest)
	if o, err := do.MustInvoke[*modstore.Store](i).InstallLocal(ctx, modstore.Build{Module: "local/probe", Wasm: wasm, Manifest: manifest,
		Language: "go", By: "telegram:user:42"}, modstore.Digest(wasm, manifest)); err != nil || o.Result != modstore.ResultInstalled {
		t.Fatalf("install: %+v, %v", o, err)
	}
	svc := do.MustInvoke[*cells.Service](i)
	if err := svc.Define(ctx, cells.Def{ID: "lamp", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"1", "2", "3"} {
		if _, err := svc.Report(ctx, "lamp", json.RawMessage(v), cells.CauseExternal, "probe"); err != nil {
			t.Fatal(err)
		}
	}
	wantCell, err := svc.Get(ctx, "lamp")
	if err != nil {
		t.Fatal(err)
	}
	wantHistory, err := svc.History(ctx, "lamp", 10)
	if err != nil || len(wantHistory) != 4 {
		t.Fatalf("the history before: %d changes, %v", len(wantHistory), err)
	}

	// The node has a daily component and a status source for the section.
	if comps := do.MustInvoke[Backups](i); len(comps) != 1 || comps[0].Name() != "backup" {
		t.Fatalf("components: %v", comps)
	}

	// `ganglion backup now`: the same Backup, on a second connection.
	key := do.MustInvoke[MasterKey](i).Key
	bk, err := BackupFor(b, do.MustInvoke[Database](i).DB, key, b.BackupHTTP, cellsExport(do.MustInvoke[*natsbus.Bus](i)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := bk.Run(ctx)
	if err != nil || !res.OK {
		t.Fatalf("backup: %+v, %v", res, err)
	}
	name, object := bucket.only()
	if !strings.HasPrefix(name, "bucket/node/ganglion-") || !strings.HasSuffix(name, ".tar.gz.enc") || res.Size != int64(len(object)) {
		t.Fatalf("object %q of %d bytes, result %+v", name, len(object), res)
	}
	for _, plain := range []string{"Anna", "SQLite format", "probe", "backup-secret-value-0001"} {
		if bytes.Contains(object, []byte(plain)) {
			t.Fatalf("the object holds %q in the clear", plain)
		}
	}
	if _, err := os.Stat(backup.WorkPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("the work folder is left: %v", err)
	}
	cs, err := do.MustInvoke[*backup.Status](i).Status(ctx, status.Scope{Now: time.Now(), Location: time.UTC})
	if err != nil || len(cs) != 1 || cs[0].State != status.OK || cs[0].Line != texts.StatusBackupOK {
		t.Fatalf("status: %+v, %v", cs, err)
	}

	// `ganglion backup restore`: into a new directory, on the same key.
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := backup.Restore(ctx, bytes.NewReader(object), key[:], restored); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restored, "master.key"), key[:], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restored, "ganglion.yaml")); err != nil {
		t.Fatalf("the settings were not restored: %v", err)
	}

	// A node started on it.
	b2 := backupNode(t, restored, srv.URL, srv.Client())
	j := New(ctx, b2, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = j.Shutdown() })
	// The cell comes back with its history, loaded from the export at this
	// start; the file that carried it is gone.
	svc2 := do.MustInvoke[*cells.Service](j)
	if c, err := svc2.Get(ctx, "lamp"); err != nil || c.Kind != cells.KindSensor || c.Version != wantCell.Version ||
		string(c.Reported) != "3" || c.ReportedCause != cells.CauseExternal {
		t.Fatalf("the cell: %+v, %v", c, err)
	}
	h, err := svc2.History(ctx, "lamp", 10)
	if err != nil || len(h) != len(wantHistory) {
		t.Fatalf("the history after: %d changes, %v", len(h), err)
	}
	for k := range h {
		if h[k].Version != wantHistory[k].Version || h[k].Field != wantHistory[k].Field ||
			string(h[k].Value) != string(wantHistory[k].Value) || h[k].Source != wantHistory[k].Source {
			t.Fatalf("change %d: %+v, want %+v", k, h[k], wantHistory[k])
		}
	}
	if _, err := os.Stat(backup.ImportDir(restored)); !os.IsNotExist(err) {
		t.Fatalf("the import folder is left: %v", err)
	}
	act, err := do.MustInvoke[*modstore.Store](j).ActiveLocal(ctx)
	if err != nil || len(act) != 1 || act[0].Module != "local/probe" || !bytes.Equal(act[0].Wasm, wasm) {
		t.Fatalf("the module: %d active, %v", len(act), err)
	}
	topics, err := memory.New(do.MustInvoke[*gitrepo.Repo](j), factGuard{}, time.Now).Topics()
	if err != nil || len(topics) != 1 || topics[0].Slug != "birthdays" || len(topics[0].Facts) != 1 || !strings.Contains(topics[0].Facts[0].Text, "Anna") {
		t.Fatalf("the memory: %+v, %v", topics, err)
	}
	if mk := do.MustInvoke[MasterKey](j); mk.Key != key || mk.Created {
		t.Fatalf("the restored node's key: created %v", mk.Created)
	}
}
