package modhost_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// linkedManifest is the probe with an output x and an input y of one shape.
const linkedManifest = probeManifest + "ports:\n  emits: {x: number}\n  accepts: {y: number}\n"

func linkedEnv(t *testing.T, ls ...settings.Link) *env {
	t.Helper()
	e := newEnv(t, active(t, "local/a", linkedManifest), active(t, "local/b", linkedManifest))
	e.conf.links = ls
	return e
}

func TestStartLogsABrokenLinkOnceAndStatusListsIt(t *testing.T) {
	good := settings.Link{From: "a.x", To: "b.y"}
	bad := settings.Link{From: "a.x", To: "c.y"}
	e := linkedEnv(t, good, bad)
	e.start(t)
	log := e.logText()
	if strings.Count(log, "a link does not work") != 1 || !strings.Contains(log, "a.x → c.y") || strings.Contains(log, "a.x → b.y") {
		t.Fatalf("log:\n%s", log)
	}
	checks, err := e.host.Status(context.Background(), status.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	var lines []status.Check
	for _, c := range checks {
		if c.Line == texts.StatusLinkBroken {
			lines = append(lines, c)
		}
	}
	if len(lines) != 1 || lines[0].Reason != texts.StatusReasonLinkEnd || lines[0].Section != status.SectionProblems ||
		lines[0].Vars["link"] != texts.Escape("a.x → c.y") || lines[0].Since != t0 {
		t.Fatalf("status %+v", checks)
	}
}

func TestStatusShowsALinkWhenNoModuleRuns(t *testing.T) {
	e := newEnv(t)
	e.conf.links = []settings.Link{{From: "a.x", To: "b.y"}}
	e.start(t)
	checks, _ := e.host.Status(context.Background(), status.Scope{})
	if !hasLine(checks, texts.StatusLinkBroken, texts.StatusReasonLinkEnd) {
		t.Fatalf("status %+v", checks)
	}
}

func TestAGoodLinkIsNoProblem(t *testing.T) {
	e := linkedEnv(t, settings.Link{From: "a.x", To: "b.y"})
	e.start(t)
	checks, _ := e.host.Status(context.Background(), status.Scope{})
	if hasLine(checks, texts.StatusLinkBroken, texts.StatusReasonLinkEnd) || strings.Contains(e.logText(), "a link does not work") {
		t.Fatalf("status %+v\n%s", checks, e.logText())
	}
}

func TestRemoveTakesTheLinksAndNamesThem(t *testing.T) {
	e := linkedEnv(t, settings.Link{From: "a.x", To: "b.y"}, settings.Link{From: "b.x", To: "a.y"})
	e.start(t)
	ctx := context.Background()
	got, err := e.host.Links(ctx, "b")
	if err != nil || !slices.Equal(got, []string{"a.x → b.y", "b.x → a.y"}) {
		t.Fatalf("links %v, %v", got, err)
	}
	if err := e.host.Remove(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if len(e.conf.links) != 0 {
		t.Fatalf("links left: %+v", e.conf.links)
	}
	if got, _ := e.host.Links(ctx, "b"); len(got) != 0 {
		t.Fatalf("links of a removed module: %v", got)
	}
}

func TestALinkToAModuleTurnedOffIsNoProblem(t *testing.T) {
	e := linkedEnv(t, settings.Link{From: "a.x", To: "b.y"})
	e.start(t)
	if err := e.host.SetOff(context.Background(), "b", true); err != nil {
		t.Fatal(err)
	}
	checks, _ := e.host.Status(context.Background(), status.Scope{})
	if hasLine(checks, texts.StatusLinkBroken, texts.StatusReasonLinkEnd) {
		t.Fatalf("status %+v", checks)
	}
}
