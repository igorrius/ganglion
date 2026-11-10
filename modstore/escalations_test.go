package modstore_test

import (
	"context"
	"testing"
)

func TestEpisodesOpenOnceClearAndReplaceByVersion(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	open := func(kind, sha string) bool {
		t.Helper()
		ok, err := s.OpenEpisode(ctx, "local/x", kind, sha, t0)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !open("auth_failed", "a") || open("auth_failed", "a") {
		t.Fatal("an episode opens once")
	}
	if !open("stale", "a") {
		t.Fatal("another kind has its own episode")
	}
	if !open("auth_failed", "b") {
		t.Fatal("a new version starts afresh")
	}
	if has, _ := s.HasEpisodes(ctx, "local/x"); !has {
		t.Fatal("no episode open")
	}
	if err := s.CloseEpisode(ctx, "local/x", "stale"); err != nil || !open("stale", "a") {
		t.Fatalf("closed episode does not open again: %v", err)
	}
	if err := s.ClearEpisodes(ctx, "local/x"); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasEpisodes(ctx, "local/x"); has {
		t.Fatal("episodes left")
	}
}
