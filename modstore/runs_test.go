package modstore_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
)

func hop(module string, at time.Time) modstore.Hop {
	return modstore.Hop{Module: module, Version: "3", Trigger: "cron", At: at, Took: 400 * time.Millisecond,
		Result: "ok", Calls: map[string]int{"http.fetch": 2}, Wrote: []string{"demo.soc"}, Sent: 1}
}

// R-RUN-20: a run is written and read back with every field.
func TestARunIsReadBackWithEveryField(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	h := modstore.Hop{Module: "local/b", Version: "1", Trigger: "cell", Tool: "b.status", Link: "a.x → b.y", Cells: []string{"a.soc"},
		At: t0, Took: 1500 * time.Millisecond, Result: "failed", Kind: "stale",
		Calls: map[string]int{"cell.get": 3, "notify": 1}, Wrote: []string{"b.x", "b.y"}, Sent: 1}
	want := modstore.Run{
		Hop: h,
		ID:  "r2", SHA256: "abc", Producer: "local/a", ProducerRun: "r1", Depth: 2,
		Chain: []modstore.Hop{hop("local/x", t0.Add(-time.Minute)), hop("local/a", t0)},
	}
	if err := s.RecordRun(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Run(ctx, "local/b", "r2")
	if err != nil || !ok {
		t.Fatalf("run: %v, %v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if _, ok, err := s.Run(ctx, "local/b", "nope"); ok || err != nil {
		t.Fatalf("unknown id: %v, %v", ok, err)
	}
}

// The same module and id is one row, the second write's.
func TestARunWrittenTwiceIsOneRow(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	a := modstore.Run{Hop: hop("local/a", t0), ID: "r1"}
	b := modstore.Run{Hop: hop("local/a", t0), ID: "r1"}
	b.Result = "failed"
	for _, r := range []modstore.Run{a, b} {
		if err := s.RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, "local/a", 10)
	if err != nil || len(runs) != 1 || runs[0].Result != "failed" {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
}

// The journal keeps the newest RunsKept of a module and touches no other.
func TestTheJournalKeepsTheNewestRunsOfAModule(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	if err := s.RecordRun(ctx, modstore.Run{Hop: hop("local/other", t0), ID: "o1"}); err != nil {
		t.Fatal(err)
	}
	for i := range 205 {
		r := modstore.Run{Hop: hop("local/a", t0.Add(time.Duration(i)*time.Second)), ID: fmt.Sprintf("r%03d", i)}
		if err := s.RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Runs(ctx, "local/a", 1000)
	if err != nil || len(all) != modstore.RunsKept {
		t.Fatalf("kept %d, %v", len(all), err)
	}
	last, err := s.Runs(ctx, "local/a", 10)
	if err != nil || len(last) != 10 || last[0].ID != "r204" || last[9].ID != "r195" {
		t.Fatalf("newest first: %+v, %v", last, err)
	}
	if _, ok, _ := s.Run(ctx, "local/a", "r004"); ok {
		t.Fatal("an old run is kept")
	}
	if _, ok, _ := s.Run(ctx, "local/other", "o1"); !ok {
		t.Fatal("another module's run was pruned")
	}
}

// Removing a local module drops its runs.
func TestRemovingAModuleDropsItsRuns(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t)
	install(t, s, build("local/x", variant("a"), withKey(localManifest)))
	if err := s.RecordRun(ctx, modstore.Run{Hop: hop("local/x", t0), ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveLocal(ctx, "local/x"); err != nil {
		t.Fatal(err)
	}
	if runs, err := s.Runs(ctx, "local/x", 10); err != nil || len(runs) != 0 {
		t.Fatalf("runs after remove: %+v, %v", runs, err)
	}
}

func TestPlainName(t *testing.T) {
	for s, want := range map[string]bool{"demo-reader.soc": true, "a": true, "Demo": false, "local/x": false, "": false, "a b": false} {
		if got := modstore.PlainName(s); got != want {
			t.Errorf("PlainName(%q) = %v", s, got)
		}
	}
}
