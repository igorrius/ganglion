package status_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

type fixed struct {
	name   string
	checks []status.Check
}

func (f fixed) Name() string                                                 { return f.name }
func (f fixed) Status(context.Context, status.Scope) ([]status.Check, error) { return f.checks, nil }

// blocking answers only after its context ends: too late.
type blocking struct{}

func (blocking) Name() string { return "slow" }
func (blocking) Status(ctx context.Context, _ status.Scope) ([]status.Check, error) {
	<-ctx.Done()
	return []status.Check{{Section: status.SectionNode, State: status.OK, Line: "late"}}, nil
}

// deaf ignores its context and answers only when told.
type deaf struct{ release chan struct{} }

func (deaf) Name() string { return "deaf" }
func (d deaf) Status(context.Context, status.Scope) ([]status.Check, error) {
	<-d.release
	return []status.Check{{Line: "too late"}}, nil
}

// failing gives one line and then an error.
type failing struct{}

func (failing) Name() string { return "queues" }
func (failing) Status(context.Context, status.Scope) ([]status.Check, error) {
	return []status.Check{{Section: status.SectionDelivery, State: status.OK, Line: "partial"}}, errors.New("database is locked")
}

type panicking struct{}

func (panicking) Name() string                                                 { return "broken" }
func (panicking) Status(context.Context, status.Scope) ([]status.Check, error) { panic("boom") }

func TestCollectKeepsTheOthersWhenOneHangsOrPanics(t *testing.T) {
	ok := fixed{"node", []status.Check{{Section: status.SectionNode, State: status.OK, Line: "a"}}}
	var logs bytes.Buffer
	got := status.Collect(t.Context(), status.Scope{}, []status.Source{ok, blocking{}, panicking{}}, 20*time.Millisecond,
		slog.New(slog.NewTextHandler(&logs, nil)))
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %+v", len(got), got)
	}
	if got[0].Line != "a" {
		t.Errorf("first line %q, want a", got[0].Line)
	}
	for _, c := range got[1:] {
		if c.State != status.Warn || c.Line != texts.StatusNoAnswer || c.Section != status.SectionProblems {
			t.Errorf("silent source gave %+v", c)
		}
	}
	if got[1].Vars["component"] != "slow" || got[2].Vars["component"] != "broken" {
		t.Errorf("components %q, %q", got[1].Vars["component"], got[2].Vars["component"])
	}
	for _, want := range []string{"source=slow", "source=broken", "boom", "goroutine"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
}

// Review: a source that ignores its context costs its line, and Collect returns.
func TestCollectDoesNotWaitForADeafSource(t *testing.T) {
	d := deaf{release: make(chan struct{})}
	defer close(d.release)
	got := status.Collect(t.Context(), status.Scope{}, []status.Source{d}, 10*time.Millisecond, slog.New(slog.DiscardHandler))
	if len(got) != 1 || got[0].Line != texts.StatusNoAnswer || got[0].Vars["component"] != "deaf" {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectKeepsTheLinesBeforeAnError(t *testing.T) {
	var logs bytes.Buffer
	got := status.Collect(t.Context(), status.Scope{}, []status.Source{failing{}}, time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
	if len(got) != 2 || got[0].Line != "partial" || got[1].Line != texts.StatusNoAnswer {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(logs.String(), "database is locked") {
		t.Errorf("log:\n%s", logs.String())
	}
}

func TestOrderPutsAnUnknownSectionLast(t *testing.T) {
	got := status.Order([]status.Check{{Section: "elsewhere", Line: "x"}, {Section: status.SectionProblems, Line: "p"}})
	if got[0].Line != "p" {
		t.Errorf("order %+v", got)
	}
}

func TestCollectWithNoSources(t *testing.T) {
	if got := status.Collect(t.Context(), status.Scope{}, nil, time.Second, slog.New(slog.DiscardHandler)); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestOrderIsBySectionAndStable(t *testing.T) {
	in := []status.Check{
		{Section: status.SectionProblems, Line: "p1"}, {Section: status.SectionNode, Line: "n1"},
		{Section: status.SectionModels, Line: "m1"}, {Section: status.SectionNode, Line: "n2"},
		{Section: status.SectionDelivery, Line: "d1"},
	}
	var got []texts.Key
	for _, c := range status.Order(in) {
		got = append(got, c.Line)
	}
	if want := []texts.Key{"n1", "n2", "m1", "d1", "p1"}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestVerdictCountsWarnAndFailNotOff(t *testing.T) {
	for _, tc := range []struct {
		states []status.State
		want   status.State
		n      int
	}{
		{nil, status.OK, 0},
		{[]status.State{status.OK, status.Off}, status.OK, 0},
		{[]status.State{status.OK, status.Warn, status.Off, status.Warn}, status.Warn, 2},
		{[]status.State{status.Warn, status.Fail}, status.Fail, 2},
	} {
		var cs []status.Check
		for _, s := range tc.states {
			cs = append(cs, status.Check{State: s})
		}
		if got, n := status.Verdict(cs); got != tc.want || n != tc.n {
			t.Errorf("%v: got %s %d, want %s %d", tc.states, got, n, tc.want, tc.n)
		}
	}
}
