package confighist_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/settings"
)

// setup is a node's file with content and a workspace repository.
func setup(t *testing.T, content string) (string, *gitrepo.Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the git binary is not installed")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, settings.FileName)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := gitrepo.Open(context.Background(), filepath.Join(dir, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	return file, repo
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecordCommitsTheCopyWithReasonAndSource(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "agent:\n  limits:\n    daily_turns: 200\n")
	h := confighist.New(file, repo, quiet())
	err := h.Record(ctx, confighist.SourcePropose, "raise the turn limit", func() error {
		return settings.SetPath(file, []settings.Change{{Path: "agent.limits.daily_turns", Value: float64(300)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := h.Changes(ctx, 10)
	if err != nil || len(cs) != 1 || cs[0].Source != confighist.SourcePropose || cs[0].Reason != "raise the turn limit" {
		t.Fatalf("Changes = %+v, %v", cs, err)
	}
	before, after, err := h.Versions(ctx, cs[0].ID)
	if err != nil || len(before) != 0 || !strings.Contains(string(after), "daily_turns: 300") {
		t.Fatalf("Versions = %q, %q, %v", before, after, err)
	}
	d, err := h.Diff(ctx, cs[0].ID)
	if err != nil || !strings.Contains(d, "+    daily_turns: 300") {
		t.Fatalf("Diff = %q, %v", d, err)
	}
}

func TestSyncCommitsAHostEditOnce(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "log:\n  level: info\n")
	h := confighist.New(file, repo, quiet())
	if err := h.Sync(ctx); err != nil { // the first start records the file as it is
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("log:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // the second finds nothing new
		if err := h.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := h.Changes(ctx, 10)
	if err != nil || len(cs) != 2 || cs[0].Source != confighist.SourceHost || cs[1].Source != confighist.SourceHost {
		t.Fatalf("Changes = %+v, %v", cs, err)
	}
}

func TestSyncRecordsAnEmptyFileOnce(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "")
	h := confighist.New(file, repo, quiet())
	for range 2 {
		if err := h.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if cs, _ := h.Changes(ctx, 10); len(cs) != 1 {
		t.Fatalf("Changes = %+v", cs)
	}
}

func TestAFailedWriteCommitsNothing(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "")
	h := confighist.New(file, repo, quiet())
	boom := errors.New("boom")
	if err := h.Record(ctx, confighist.SourcePropose, "x", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if cs, err := h.Changes(ctx, 10); err != nil || len(cs) != 0 {
		t.Fatalf("Changes = %+v, %v", cs, err)
	}
}

func TestAnIDThatIsNoChangeIsRefused(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "")
	h := confighist.New(file, repo, quiet())
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	other, _ := repo.Path("memory/x.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := repo.Commit(ctx, []string{"memory/x.md"}, "memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Versions(ctx, id); !errors.Is(err, confighist.ErrNoChange) {
		t.Fatalf("a memory commit was read as a change: %v", err)
	}
}

func TestTwoWritersLoseNoChange(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "")
	h := confighist.New(file, repo, quiet())
	f := confighist.ModulesFile{File: settings.ModulesFile{Path: file}, History: h}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := f.SetModule(settings.Module{Name: fmt.Sprintf("local/m%d", i)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ms, err := f.Modules()
	if err != nil || len(ms) != 20 {
		t.Fatalf("Modules = %d, %v", len(ms), err)
	}
	copyPath, _ := repo.Path(confighist.CopyPath)
	live, _ := os.ReadFile(file)
	copied, _ := os.ReadFile(copyPath)
	if string(live) != string(copied) {
		t.Fatal("the copy differs from the live file")
	}
	if cs, _ := h.Changes(ctx, 50); len(cs) != 20 {
		t.Fatalf("%d changes, want 20", len(cs))
	}
}

func TestNoHistory(t *testing.T) {
	ran := false
	h := confighist.NoHistory{}
	if err := h.Record(context.Background(), confighist.SourceHost, "x", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatal("NoHistory does not write")
	}
	if _, err := h.Changes(context.Background(), 1); !errors.Is(err, confighist.ErrNoHistory) {
		t.Fatalf("err = %v", err)
	}
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}
