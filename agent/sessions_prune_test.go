package agent

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

func archiveDB(t *testing.T) (*Archive, *ReviewDue) {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a, err := NewArchive(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewReviewDue(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return a, d
}

func archived(t *testing.T, a *Archive, conv, session string, ended time.Time) {
	t.Helper()
	s := State{V: StateVersion, Session: session, Started: ended.Add(-time.Hour), Last: ended,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
	if err := a.Put(context.Background(), conv, s, "idle"); err != nil {
		t.Fatal(err)
	}
}

func sessionsOf(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT session FROM sessions ORDER BY session`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// #245: a session that ended before the cut goes, unless the memory review still has to read it; a
// newer one stays (R-MEM-1, ADR-0104).
func TestArchivePruneKeepsWhatTheReviewHasNotRead(t *testing.T) {
	ctx := context.Background()
	a, d := archiveDB(t)
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	cut := now.Add(-90 * 24 * time.Hour)
	archived(t, a, "telegram:1", "old", cut.Add(-time.Hour))
	archived(t, a, "telegram:1", "unread", cut.Add(-time.Hour))
	archived(t, a, "telegram:2", "new", cut.Add(time.Hour))
	if err := d.Arm(ctx, "telegram:1", "unread", "telegram", "telegram:user:1", now); err != nil {
		t.Fatal(err)
	}
	n, err := a.Prune(ctx, cut)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionsOf(t, a.db); n != 1 || len(got) != 2 || got[0] != "new" || got[1] != "unread" {
		t.Fatalf("pruned %d, left %v", n, got)
	}
}

// The pruner drops what is past its age on each pass and counts it; an age of 0 keeps every session.
func TestArchivePrunerPass(t *testing.T) {
	ctx := context.Background()
	reader := telemetrytest.Install(t)
	a, _ := archiveDB(t)
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	archived(t, a, "telegram:1", "old", now.Add(-91*24*time.Hour))
	keep := ArchivePruner{Archive: a, Keep: 0, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	keep.pass(ctx)
	if got := sessionsOf(t, a.db); len(got) != 1 {
		t.Fatalf("an age of 0 dropped %v", got)
	}
	p := ArchivePruner{Archive: a, Keep: 90 * 24 * time.Hour, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	p.pass(ctx)
	if got := sessionsOf(t, a.db); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
	if got := reader.Value(t, "ganglion.sessions.pruned", map[string]string{"outcome": "ok"}); got != 1 {
		t.Errorf("counted %v", got)
	}
}
