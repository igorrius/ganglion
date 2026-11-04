package skills_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/skills"
	"github.com/igorrius/ganglion/sqlitedb"
)

// A key that began stays begun across a restart, until it is old.
func TestRunsRememberAKeyAcrossARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.db")
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	open := func() *skills.Runs {
		db, err := sqlitedb.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		r, err := skills.OpenRuns(ctx, db, clock)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	begin := func(r *skills.Runs, key string, want bool) {
		t.Helper()
		if got, err := r.Begin(ctx, key, "clean-cache"); err != nil || got != want {
			t.Fatalf("Begin(%s) = %t, %v; want %t", key, got, err, want)
		}
	}
	r := open()
	begin(r, "E1:1", true)
	begin(r, "E1:1", false)
	begin(r, "E1:2", true)
	begin(open(), "E1:1", false)
	now = now.Add(31 * 24 * time.Hour)
	begin(r, "E1:1", true)
}
