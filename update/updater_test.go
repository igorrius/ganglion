package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// listing is a Releases over a fakeSource and a fixed newest release.
type listing struct {
	*fakeSource
	newest Release
	err    error
}

func (l listing) Latest(context.Context) (Release, error) { return l.newest, l.err }

func updaterKit(t *testing.T, running string) (*Updater, *kit, *int) {
	t.Helper()
	k := newKit(t)
	v, _ := ParseVersion("v0.3.0")
	src := listing{fakeSource: k.src, newest: Release{Version: v, Tag: "v0.3.0"}}
	opened := 0
	open := func() (*Store, error) { opened++; return k.store, nil }
	// newKit opened the store, which made the folder; the lazy tests want it
	// to be absent until the updater needs it.
	dir := filepath.Join(t.TempDir(), "releases")
	return NewUpdater(src, dir, func() (*Store, error) { _ = os.MkdirAll(dir, 0o700); return open() }, running, WithImage(true)), k, &opened
}

func TestUpdaterStatus(t *testing.T) {
	u, _, opened := updaterKit(t, "v0.2.1")
	st, err := u.Status(context.Background())
	if err != nil || st.Dev || !st.Newer || !st.Image || st.Running.String() != "v0.2.1" || st.Newest.Version.String() != "v0.3.0" {
		t.Fatalf("%+v %v", st, err)
	}
	if *opened != 0 {
		t.Fatal("a status opened the store")
	}
	u, _, _ = updaterKit(t, "v0.3.0")
	if st, _ := u.Status(context.Background()); st.Newer {
		t.Fatalf("the newest is not newer than itself: %+v", st)
	}
}

func TestUpdaterStatusOfADevBuildKeepsTheNewest(t *testing.T) {
	u, _, _ := updaterKit(t, "v0.2.1-3-gabc")
	st, err := u.Status(context.Background())
	if err != nil || !st.Dev || st.Newest.Version.String() != "v0.3.0" || st.Newer {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := u.Install(context.Background(), "v0.3.0"); !errors.Is(err, ErrDevBuild) {
		t.Fatalf("install on a dev build: %v", err)
	}
}

func TestUpdaterStatusErrorIsTheSources(t *testing.T) {
	u, _, _ := updaterKit(t, "v0.2.1")
	u.src = listing{fakeSource: &fakeSource{}, err: ErrFetch}
	if _, err := u.Status(context.Background()); !errors.Is(err, ErrFetch) {
		t.Fatal(err)
	}
	u, _, _ = updaterKit(t, "dev")
	u.src = listing{fakeSource: &fakeSource{}, err: ErrFetch}
	if st, err := u.Status(context.Background()); err != nil || !st.Dev || !st.Newest.Version.IsZero() {
		t.Fatalf("a dev build that cannot reach GitHub: %+v %v", st, err)
	}
}

// A node that never updated has no releases folder: a rollback says there is
// nothing and opens nothing.
func TestUpdaterRollbackOfANodeThatNeverUpdatedOpensNothing(t *testing.T) {
	u, _, opened := updaterKit(t, "v0.2.1")
	if _, err := u.Rollback(context.Background()); !errors.Is(err, ErrNoPrevious) {
		t.Fatal(err)
	}
	if *opened != 0 {
		t.Fatal("a rollback opened the store")
	}
	if _, err := os.Stat(u.dir); err == nil {
		t.Fatal("the releases folder was made")
	}
}

func TestUpdaterInstallsThenRollsBack(t *testing.T) {
	u, k, opened := updaterKit(t, "v0.2.1")
	got, err := u.Install(context.Background(), "v0.3.0")
	if err != nil || got != "v0.3.0" || *opened != 1 {
		t.Fatalf("%q %v opened %d", got, err, *opened)
	}
	// Make the state the updater's own folder sees, as one store serves both.
	if err := os.WriteFile(filepath.Join(u.dir, StateFile), []byte(`{"active":"","previous":"","confirmed":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if to, err := u.Rollback(context.Background()); err != nil || to != "" {
		t.Fatalf("rollback to %q: %v", to, err)
	}
	if st, _ := k.store.State(); st.Active != "" {
		t.Fatalf("state %+v", st)
	}
}

func TestUpdaterRollbackTarget(t *testing.T) {
	u, _, opened := updaterKit(t, "v0.2.1")
	if _, ok := u.RollbackTarget(); ok {
		t.Fatal("a node that never updated has a target")
	}
	st, err := u.Status(context.Background())
	if err != nil || st.CanRollback || *opened != 0 {
		t.Fatalf("%+v %v opened %d", st, err, *opened)
	}
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	u.open = func() (*Store, error) { return Open(u.dir) } // the store of the updater's own folder
	write := func(state string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(u.dir, StateFile), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"active":"v0.3.0","previous":"","confirmed":""}`)
	if to, ok := u.RollbackTarget(); !ok || to != "" {
		t.Fatalf("image target: %q %v", to, ok)
	}
	if st, _ := u.Status(context.Background()); !st.CanRollback || st.RollbackTo != "" {
		t.Fatalf("%+v", st)
	}
	write(`{"active":"v0.3.0","previous":"v0.2.5","confirmed":""}`)
	if _, ok := u.RollbackTarget(); ok {
		t.Fatal("a previous release that is not on the disk is a target")
	}
	write(`{"active":"","previous":"","confirmed":""}`)
	if _, ok := u.RollbackTarget(); ok {
		t.Fatal("an empty state has a target")
	}
}
