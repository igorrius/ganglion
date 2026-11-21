package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cellsExport = `{"key":"lamp","seq":1,"op":"put","value":"e30="}` + "\n"

// exportOf is an ExportFunc that writes text.
func exportOf(text string) ExportFunc {
	return FileExport(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func restoreInto(t *testing.T, archive, key []byte) (string, Manifest) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "restored")
	m, err := Restore(context.Background(), bytes.NewReader(archive), key, dst)
	if err != nil {
		t.Fatal(err)
	}
	return dst, m
}

// The backup exports the bucket after it copied the database, the archive
// holds it with its hash, and a restore leaves it in the import folder, not
// in the bus's store folder.
func TestRunPutsTheCellsExportInTheArchiveAndRestoreLeavesItForTheStart(t *testing.T) {
	n := newNode(t)
	up := &fakeUploader{dir: n.dir}
	var sawSnapshot bool
	cfg := DefaultConfig(n.dir)
	b := New(n.db, up, testKey(3), cfg, WithBackupClock(func() time.Time { return testTime }),
		WithExport(func(ctx context.Context, path string) error {
			_, err := os.Stat(filepath.Join(filepath.Dir(path), dbName))
			sawSnapshot = err == nil
			return exportOf(cellsExport)(ctx, path)
		}))
	if _, err := b.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawSnapshot {
		t.Error("the export was made before the database copy")
	}
	dst, m := restoreInto(t, up.body, testKey(3))
	if fi, ok := m.Files[cellsName]; !ok || fi.Size != int64(len(cellsExport)) {
		t.Fatalf("the manifest: %+v", m.Files)
	}
	info, err := os.Stat(ImportPath(dst))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the import file: %v, %v", info, err)
	}
	if got := readFile(t, ImportPath(dst)); got != cellsExport {
		t.Fatalf("the import file holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, busName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the restore made the bus's own folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(n.dir, workDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the work folder is left: %v", err)
	}
}

// A failed export is a failed backup: nothing is uploaded.
func TestRunFailsWhenTheExportFails(t *testing.T) {
	n := newNode(t)
	up := &fakeUploader{dir: n.dir}
	boom := errors.New("bus is gone")
	b := New(n.db, up, testKey(3), DefaultConfig(n.dir), WithBackupClock(func() time.Time { return testTime }),
		WithExport(func(context.Context, string) error { return boom }))
	res, err := b.Run(context.Background())
	if !errors.Is(err, boom) || res.OK || len(up.keys) != 0 {
		t.Fatalf("%+v, %v, %d uploads", res, err, len(up.keys))
	}
}

// An archive made before cells moved to the bus has no export, and a restore
// of it writes no import folder.
func TestRestoreOfAnArchiveWithoutTheCellsExportLeavesNoImport(t *testing.T) {
	n := newNode(t)
	dst, m := restoreInto(t, packSealed(t, n, testKey(3)), testKey(3))
	if _, ok := m.Files[cellsName]; ok {
		t.Fatal("the archive holds an export")
	}
	if _, err := os.Stat(ImportDir(dst)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the import folder: %v", err)
	}
}

// The cells entry is the one file of the archive's bus folder: another name
// in it, or a changed export, is refused.
func TestRestoreRefusesOtherFilesInTheBusFolderAndAChangedExport(t *testing.T) {
	key := testKey(7)
	good := file(dbName, realDB(t))
	cells := file(cellsName, cellsExport)
	changed := okManifest(good, cells)
	changed.Files[cellsName] = FileInfo{SHA256: strings.Repeat("0", 64), Size: int64(len(cellsExport))}
	for name, tc := range map[string]struct {
		m       Manifest
		entries []tarEntry
		want    error
	}{
		"another file":  {okManifest(good, file("bus/store.bin", "x")), []tarEntry{good, file("bus/store.bin", "x")}, ErrUnsafe},
		"a deeper file": {okManifest(good, file("bus/jetstream/x", "x")), []tarEntry{good, file("bus/jetstream/x", "x")}, ErrUnsafe},
		"changed":       {changed, []tarEntry{good, cells}, ErrManifest},
		"not listed":    {okManifest(good), []tarEntry{good, cells}, ErrManifest},
	} {
		parent := t.TempDir()
		_, err := Restore(context.Background(), bytes.NewReader(craft(t, key, tc.m, tc.entries...)), key, filepath.Join(parent, "data"))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		assertClean(t, parent)
	}
}

// ImportPending hands the file to the import, then removes it and its
// folder; a failure keeps the file.
func TestImportPending(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	t.Run("nothing to import", func(t *testing.T) {
		called := false
		n, err := ImportPending(context.Background(), t.TempDir(), func(context.Context, io.Reader) (int, error) {
			called = true
			return 0, nil
		}, log)
		if err != nil || n != 0 || called {
			t.Fatalf("%d, %v, called %v", n, err, called)
		}
	})
	t.Run("imported and removed", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, ImportPath(dir), cellsExport, 0o600)
		var got string
		n, err := ImportPending(context.Background(), dir, func(_ context.Context, r io.Reader) (int, error) {
			b, _ := io.ReadAll(r)
			got = string(b)
			return 1, nil
		}, log)
		if err != nil || n != 1 || got != cellsExport {
			t.Fatalf("%d, %v, %q", n, err, got)
		}
		if _, err := os.Stat(ImportDir(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the folder is left: %v", err)
		}
	})
	t.Run("a failure keeps the file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, ImportPath(dir), cellsExport, 0o600)
		refused := errors.New("the bucket holds 3 entries")
		_, err := ImportPending(context.Background(), dir, func(context.Context, io.Reader) (int, error) { return 0, refused }, log)
		if !errors.Is(err, refused) {
			t.Fatalf("error %v", err)
		}
		if got := readFile(t, ImportPath(dir)); got != cellsExport {
			t.Fatalf("the file holds %q", got)
		}
	})
}
