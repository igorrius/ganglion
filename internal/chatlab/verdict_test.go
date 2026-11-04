package chatlab

import (
	"strings"
	"testing"
)

func passing() Repeat { return Repeat{} }

func judged(oks ...bool) Repeat {
	r := Repeat{}
	for i, ok := range oks {
		r.Judgments = append(r.Judgments, Judgment{Claim: []string{"Russian", "not saved"}[i], OK: ok})
	}
	return r
}

func TestDecide(t *testing.T) {
	inconclusive := Repeat{Inconclusive: "memory.write was never called"}
	failed := Repeat{Failures: []string{"questions: wanted 1 showing, found 2"}}
	for _, tc := range []struct {
		name    string
		repeats []Repeat
		want    Result
		cell    string // in Facts or Meaning
	}{
		{"facts only", []Repeat{passing(), passing(), passing()}, ResultPassed, "3 of 3"},
		{"every judgment", []Repeat{judged(true), judged(true), judged(true)}, ResultPassed, "Russian 3 of 3"},
		{"a fact failed", []Repeat{passing(), failed, passing()}, ResultFailed, "found 2"},
		{"judge differs", []Repeat{judged(true), judged(false), judged(true), judged(false), judged(false)}, ResultFlaky, "Russian 2 of 5"},
		{"judge in no repeat", []Repeat{judged(false), judged(false), judged(false)}, ResultFailed, "Russian 0 of 3"},
		{"the worst of two", []Repeat{judged(true, true), judged(true, false), judged(true, false)}, ResultFlaky, "not saved 1 of 3"},
		{"the worst fails", []Repeat{judged(true, false), judged(true, false)}, ResultFailed, "not saved 0 of 2"},
		{"all inconclusive", []Repeat{inconclusive, inconclusive}, ResultInconclusive, "0 of 2"},
		{"some inconclusive", []Repeat{judged(true), judged(true), inconclusive}, ResultInconclusive, "2 of 2, 1 inconclusive"},
		{"some inconclusive and flaky", []Repeat{judged(true), judged(false), inconclusive}, ResultFlaky, "1 of 2, 1 inconclusive"},
		{"a failure beats inconclusive", []Repeat{failed, inconclusive}, ResultFailed, "found 2"},
		{"no repeats", nil, ResultNotRun, ""},
		{"not run", []Repeat{{NotRun: "the token cap"}}, ResultNotRun, "token cap"},
		{"not run counts", []Repeat{{NotRun: "the token cap"}}, ResultNotRun, "0 of 1"},
		{"some not run", []Repeat{passing(), {NotRun: "the token cap"}}, ResultNotRun, "1 of 1, 1 not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := Decide("s", "w", tc.repeats)
			if o.Result != tc.want {
				t.Fatalf("result %s, want %s (%+v)", o.Result, tc.want, o)
			}
			if !strings.Contains(o.Facts+" "+o.Meaning, tc.cell) {
				t.Fatalf("cells %q / %q do not hold %q", o.Facts, o.Meaning, tc.cell)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		results []Result
		want    int
	}{
		{[]Result{ResultPassed, ResultPassed}, 0},
		{nil, 2},
		{[]Result{ResultPassed, ResultFailed}, 1},
		{[]Result{ResultFlaky, ResultFailed, ResultNotRun}, 1},
		{[]Result{ResultPassed, ResultFlaky}, 2},
		{[]Result{ResultInconclusive}, 2},
		{[]Result{ResultNotRun}, 2},
	} {
		os := make([]Outcome, len(tc.results))
		for i, r := range tc.results {
			os[i].Result = r
		}
		if got := ExitCode(os, nil); got != tc.want {
			t.Errorf("%v: exit %d, want %d", tc.results, got, tc.want)
		}
	}
}
