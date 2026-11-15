package config

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/update"
	"github.com/samber/do/v2"
)

// The update watch runs on a node that has a releases state, and a node
// that never updated gets none and no releases folder.
func TestUpdateWatchRunsOnlyWithAReleasesState(t *testing.T) {
	b := testConfig(t)
	dir, _ := update.DirFor(b.DB)
	for _, withState := range []bool{false, true} {
		if withState {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, update.StateFile), []byte(`{"active":"","previous":"","confirmed":""}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		i := New(context.Background(), b, slog.New(slog.DiscardHandler))
		comps, err := do.Invoke[Updates](i)
		if err != nil {
			t.Fatal(err)
		}
		if withState != (len(comps) == 1 && comps[0].Name() == "update") {
			t.Fatalf("with state %v: %v", withState, comps)
		}
		if _, err := os.Stat(dir); !withState && err == nil {
			t.Fatal("the releases folder was created")
		}
		if rep := i.Shutdown(); !rep.Succeed {
			t.Fatal(rep.Error())
		}
	}
}

// /update on a node that never updated shows its status and refuses a
// rollback without making a releases folder; a node that keeps nothing on
// disk gets the no-op updater.
func TestUpdaterMakesNoReleasesFolderUntilAnInstall(t *testing.T) {
	b := testConfig(t)
	dir, _ := update.DirFor(b.DB)
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	defer func() { _ = i.Shutdown() }()
	u, err := do.Invoke[agent.Updater](i)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := u.(*update.Updater); !ok {
		t.Fatalf("updater %T", u)
	}
	if _, err := u.Rollback(context.Background()); !errors.Is(err, update.ErrNoPrevious) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the releases folder was created")
	}

	b.DB = ":memory:"
	mem := New(context.Background(), b, slog.New(slog.DiscardHandler))
	defer func() { _ = mem.Shutdown() }()
	if u, err := do.Invoke[agent.Updater](mem); err != nil || u != (agent.NoUpdater{}) {
		t.Fatalf("in memory: %T %v", u, err)
	}
}

// The daily check is a component only when update.check is on and the
// database is a file; it makes no releases folder by itself.
func TestUpdateCheckRunsOnlyWhenOnAndOnAFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		on    bool
		mem   bool
		wants bool
	}{{"on", true, false, true}, {"off", false, false, false}, {"in memory", true, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			b := testConfig(t)
			b.UpdateCheck = tc.on
			if tc.mem {
				b.DB = ":memory:"
			}
			dir, _ := update.DirFor(b.DB)
			i := New(context.Background(), b, slog.New(slog.DiscardHandler))
			defer func() { _ = i.Shutdown() }()
			comps, err := do.Invoke[UpdateChecks](i)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wants != (len(comps) == 1 && comps[0].Name() == "update-check") {
				t.Fatalf("%v", comps)
			}
			if _, err := os.Stat(dir); err == nil && dir != "" {
				t.Fatal("the releases folder was created")
			}
		})
	}
}
