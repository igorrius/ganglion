package modstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/modstore"
)

// linkManifest is a module with the output x and the input y of the given
// shapes.
func linkManifest(name string, version int, emits, accepts string) string {
	return fmt.Sprintf("name: %s\nversion: %d\nabi: 1\nports:\n  emits: {x: %s}\n  accepts: {y: %s}\n", name, version, emits, accepts)
}

func useLinks(s *modstore.Store, ls ...links.Link) {
	s.UseLinks(func() ([]links.Link, error) { return ls, nil })
}

func TestABuildThatBreaksALinkIsRefused(t *testing.T) {
	s := newEnv(t).open(t)
	install(t, s, build("local/a", variant("a1"), linkManifest("a", 1, "number", "number")))
	install(t, s, build("local/b", variant("b1"), linkManifest("b", 1, "number", "number")))
	useLinks(s, links.Link{From: "a.x", To: "b.y"})

	// the output changes its shape
	v2 := build("local/a", variant("a2"), linkManifest("a", 2, "string", "number"))
	p := plan(t, s, v2, noOwners)
	if p.Refusal != "the build breaks a link" || !strings.Contains(p.RefusalDetail, "a.x → b.y") || !strings.Contains(p.RefusalDetail, "shapes differ") {
		t.Fatalf("plan refusal %q detail %q", p.Refusal, p.RefusalDetail)
	}
	o, err := s.InstallLocal(context.Background(), v2, modstore.Digest(v2.Wasm, v2.Manifest))
	if err != nil || o.Result != modstore.ResultRefused || !strings.Contains(o.Reason, "a.x → b.y") {
		t.Fatalf("install %+v, %v", o, err)
	}

	// the output is gone
	gone := build("local/a", variant("a3"), "name: a\nversion: 3\nabi: 1\n")
	if p := plan(t, s, gone, noOwners); p.Refusal != "the build breaks a link" || !strings.Contains(p.RefusalDetail, "no port x") {
		t.Fatalf("plan refusal %q detail %q", p.Refusal, p.RefusalDetail)
	}

	// the same ports pass
	same := build("local/a", variant("a4"), linkManifest("a", 4, "number", "number"))
	if p := plan(t, s, same, noOwners); p.Refusal != "" {
		t.Fatalf("refused: %s", p.Refusal)
	}
}

func TestALinkBrokenBeforeTheBuildDoesNotRefuseIt(t *testing.T) {
	s := newEnv(t).open(t)
	install(t, s, build("local/a", variant("a1"), linkManifest("a", 1, "number", "number")))
	useLinks(s, links.Link{From: "a.x", To: "gone.y"})
	next := build("local/a", variant("a2"), linkManifest("a", 2, "number", "number"))
	if p := plan(t, s, next, noOwners); p.Refusal != "" {
		t.Fatalf("refused: %s %s", p.Refusal, p.RefusalDetail)
	}
}

func TestALinkWrittenAheadIsMendedByTheModuleThatInstalls(t *testing.T) {
	s := newEnv(t).open(t)
	install(t, s, build("local/a", variant("a1"), linkManifest("a", 1, "number", "number")))
	useLinks(s, links.Link{From: "a.x", To: "b.y"})
	if p := plan(t, s, build("local/b", variant("b1"), linkManifest("b", 1, "number", "number")), noOwners); p.Refusal != "" {
		t.Fatalf("refused: %s", p.Refusal)
	}
}

func TestABuildThatClosesALoopThroughLinksIsRefused(t *testing.T) {
	s := newEnv(t).open(t)
	install(t, s, build("local/a", variant("a1"), linkManifest("a", 1, "number", "number")))
	install(t, s, build("local/b", variant("b1"), linkManifest("b", 1, "number", "number")))
	useLinks(s, links.Link{From: "a.x", To: "b.y"})
	// a listens to a cell of b: b → a by cell, a → b by link
	next := build("local/a", variant("a2"), "name: a\nversion: 2\nabi: 1\ntriggers:\n  - cell: [b.v]\ncells:\n  read: [b.v]\n"+
		"ports:\n  emits: {x: number}\n  accepts: {y: number}\n")
	p := plan(t, s, next, noOwners)
	if p.Refusal != "the links would form a loop" || p.RefusalDetail != "a → b → a" {
		t.Fatalf("refusal %q detail %q", p.Refusal, p.RefusalDetail)
	}
}

func TestAChainDeeperThanFourIsRefused(t *testing.T) {
	s := newEnv(t).open(t)
	var ls []links.Link
	for i, n := range []string{"a", "b", "c", "d"} {
		install(t, s, build("local/"+n, variant(n+"1"), linkManifest(n, 1, "number", "number")))
		if i > 0 {
			ls = append(ls, links.Link{From: string(rune('a'+i-1)) + ".x", To: n + ".y"})
		}
	}
	useLinks(s, append(ls, links.Link{From: "d.x", To: "e.y"})...)
	p := plan(t, s, build("local/e", variant("e1"), linkManifest("e", 1, "number", "number")), noOwners)
	if p.Refusal != "the build would make a chain of modules that is too deep" || p.RefusalDetail != "a → b → c → d → e" {
		t.Fatalf("refusal %q detail %q", p.Refusal, p.RefusalDetail)
	}
}
