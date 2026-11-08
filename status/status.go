// Package status is what the node says of itself for /status (R-OBS-11): each
// component reports its own lines through Source, and Collect asks them all at
// once. A line's text and reason are fixed texts with values, never an error's
// text, which can hold outside content.
package status

import (
	"context"
	"slices"
	"time"

	"github.com/igorrius/ganglion/texts"
)

// Section is where a line goes.
type Section string

// The sections, in the order they are shown.
const (
	SectionNode     Section = "node"
	SectionModels   Section = "models"
	SectionDelivery Section = "delivery"
	SectionProblems Section = "problems"
)

// Sections are the sections in the order they are shown.
var Sections = []Section{SectionNode, SectionModels, SectionDelivery, SectionProblems}

// State is how a line stands. Off is switched off by configuration and
// raises nothing.
type State string

// The states.
const (
	OK   State = "ok"
	Warn State = "warn"
	Off  State = "off"
	Fail State = "fail"
)

// Check is one line of /status.
type Check struct {
	Section Section
	State   State
	Line    texts.Key  // the line's text
	Reason  texts.Key  // why it is not OK; "" when there is none
	Vars    texts.Vars // the values of Line and Reason, already formatted
	Since   time.Time  // when the state began; zero when unknown
}

// Scope is who asked and when.
type Scope struct {
	Conv, Principal string
	Now             time.Time
	Location        *time.Location
}

// Source is a component that reports its own lines. Status must honour ctx.
type Source interface {
	// Name names the component in the line that says it did not answer; a
	// fixed word such as "telegram".
	Name() string
	Status(ctx context.Context, s Scope) []Check
}

// Collect asks every source at once, each under its own timeout. A source that
// does not answer in time or panics becomes one Warn line naming it, and the
// others are kept, in the sources' order. A source that ignores ctx keeps its
// goroutine until it returns, and its answer is dropped.
func Collect(ctx context.Context, s Scope, sources []Source, timeout time.Duration) []Check {
	results := make([]chan []Check, len(sources))
	for i, src := range sources {
		results[i] = make(chan []Check, 1)
		go ask(ctx, s, src, timeout, results[i])
	}
	// One deadline for the wait, a little past the sources' own, so a source
	// that ignores its context costs its line and no more.
	wait, cancel := context.WithTimeout(ctx, timeout+timeout/2)
	defer cancel()
	var out []Check
	for i, src := range sources {
		select {
		case cs, ok := <-results[i]:
			if ok {
				out = append(out, cs...)
				continue
			}
		case <-wait.Done():
		}
		out = append(out, silent(src.Name()))
	}
	return out
}

// ask runs one source into ch; a panic or a late answer closes ch with
// nothing sent, which Collect reads as silence.
func ask(ctx context.Context, s Scope, src Source, timeout time.Duration, ch chan<- []Check) {
	defer func() {
		if recover() != nil {
			close(ch)
		}
	}()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cs := src.Status(cctx, s)
	if cctx.Err() != nil {
		close(ch)
		return
	}
	ch <- cs
}

// silent is the line of a source that did not answer.
func silent(name string) Check {
	return Check{Section: SectionProblems, State: Warn, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": name}}
}

// Order sorts the lines by section, keeping the order within each.
func Order(checks []Check) []Check {
	out := slices.Clone(checks)
	slices.SortStableFunc(out, func(a, b Check) int {
		return slices.Index(Sections, a.Section) - slices.Index(Sections, b.Section)
	})
	return out
}

// Verdict is Fail if any line fails, else Warn if any warns, else OK, with
// the count of the lines that warn or fail. Off counts for nothing.
func Verdict(checks []Check) (State, int) {
	v, n := OK, 0
	for _, c := range checks {
		switch c.State {
		case Fail:
			v, n = Fail, n+1
		case Warn:
			if v != Fail {
				v = Warn
			}
			n++
		case OK, Off:
		}
	}
	return v, n
}
