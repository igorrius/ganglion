package status_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

type fixed struct {
	name   string
	checks []status.Check
}

func (f fixed) Name() string                                        { return f.name }
func (f fixed) Status(context.Context, status.Scope) []status.Check { return f.checks }

// blocking answers only after its context ends: too late.
type blocking struct{}

func (blocking) Name() string { return "slow" }
func (blocking) Status(ctx context.Context, _ status.Scope) []status.Check {
	<-ctx.Done()
	return []status.Check{{Section: status.SectionNode, State: status.OK, Line: "late"}}
}

type panicking struct{}

func (panicking) Name() string                                        { return "broken" }
func (panicking) Status(context.Context, status.Scope) []status.Check { panic("boom") }

func TestCollectKeepsTheOthersWhenOneHangsOrPanics(t *testing.T) {
	ok := fixed{"node", []status.Check{{Section: status.SectionNode, State: status.OK, Line: "a"}}}
	got := status.Collect(t.Context(), status.Scope{}, []status.Source{ok, blocking{}, panicking{}}, 20*time.Millisecond)
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
}

func TestCollectWithNoSources(t *testing.T) {
	if got := status.Collect(t.Context(), status.Scope{}, nil, time.Second); len(got) != 0 {
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
