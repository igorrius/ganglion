// Package links checks the links of the node's file against the ports of
// the active modules (R-BUS-5): that both ends exist, that their kinds
// match, that their shapes are equal, and that the graph of links and cell
// triggers has no cycle and no chain deeper than MaxDepth. It is pure: the
// callers read the manifests and the file and decide what a problem means.
package links

import (
	"fmt"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/runner"
)

// MaxDepth is the longest chain of modules a link or a cell trigger starts
// one from another: the number of modules on the longest path (M4c design
// section 4, R-BUS-5). The module host stops a run's cells at this depth.
const MaxDepth = 4

// Link joins an output (or a calling port) to an input (or a serving
// port); each end is `<module>.<port>` with the module's short name. It is
// settings.Link without the file's tags.
type Link struct {
	From, To string
}

// String is the link as the node writes it: `a.x → b.y`.
func (l Link) String() string { return l.From + " → " + l.To }

// Touches reports whether either end is a port of the module (its short
// name).
func (l Link) Touches(module string) bool {
	return moduleOf(l.From) == module || moduleOf(l.To) == module
}

// Module is what a link needs to know of an active local module: its
// ports, with the implicit `notify` output among Emits, and the cells it
// listens to.
type Module struct {
	Emits   map[string]runner.Shape
	Accepts map[string]runner.Shape
	Serves  map[string]runner.PortCall
	Calls   map[string]runner.PortCall
	Listens []string
}

// FromManifest is the Module a manifest describes.
func FromManifest(m runner.Manifest) Module {
	return Module{Emits: m.Emits(), Accepts: m.Ports.Accepts, Serves: m.Ports.Serves, Calls: m.Ports.Calls, Listens: m.Listens()}
}

// Reason is what is wrong with a link, a closed set.
type Reason string

// The reasons.
const (
	// ReasonEnd: a module is not active, or has no such port.
	ReasonEnd Reason = "end"
	// ReasonKind: the ends are not an output and an input of the same kind
	// (emits to accepts, calls to serves).
	ReasonKind Reason = "kind"
	// ReasonShape: the shapes of the two ports differ.
	ReasonShape Reason = "shape"
	// ReasonTaken: the calling port is linked already; a calling port has at
	// most one link (R-BUS-6).
	ReasonTaken Reason = "taken"
	// ReasonLoop: the link closes a cycle through links and cell triggers.
	ReasonLoop Reason = "loop"
	// ReasonDeep: a chain of modules is deeper than MaxDepth.
	ReasonDeep Reason = "deep"
)

// Problem is one thing wrong. Link is the link at fault; for a loop or a
// chain it is the first link on the path, and it is the zero Link when the
// path runs through cell triggers alone.
type Problem struct {
	Link   Link
	Reason Reason
	// Detail says which end or what differs, for ReasonEnd, ReasonKind and
	// ReasonShape.
	Detail string
	// Path is the modules of a loop (the first repeated at the end) or of
	// the longest chain.
	Path []string
}

// Involves reports whether the problem is the module's: its link touches
// it, or it is on the path.
func (p Problem) Involves(module string) bool {
	if p.Link.Touches(module) {
		return true
	}
	return slices.Contains(p.Path, module)
}

// Words is the problem in the node's fixed words (English, for the model
// and the journal).
func (p Problem) Words() string {
	var why string
	switch p.Reason {
	case ReasonEnd:
		why = p.Detail
	case ReasonKind:
		why = "the kinds do not match: " + p.Detail
	case ReasonShape:
		why = "the shapes differ: " + p.Detail
	case ReasonTaken:
		why = "the calling port is linked already: " + p.Detail
	case ReasonLoop:
		why = "it closes a loop: " + strings.Join(p.Path, " → ")
	case ReasonDeep:
		why = fmt.Sprintf("it makes a chain deeper than %d modules: %s", MaxDepth, strings.Join(p.Path, " → "))
	}
	if p.Link == (Link{}) {
		return why
	}
	return p.Link.String() + ": " + why
}

// Check reports what is wrong with the links among the modules, by short
// name: first each link on its own, in the order given, then the graph. A
// link that is wrong on its own is left out of the graph, so one bad line
// does not condemn the rest.
func Check(mods map[string]Module, ls []Link) []Problem {
	var out []Problem
	var good []Link
	callers := map[string]Link{} // the first good link of each calling port
	for _, l := range ls {
		p, bad := checkLink(mods, l)
		if !bad {
			bad = takenCaller(callers, mods, l, &p)
		}
		if bad {
			out = append(out, p)
		} else {
			good = append(good, l)
		}
	}
	return append(out, checkGraph(mods, good)...)
}

// takenCaller reports whether l starts at a calling port that an earlier
// good link starts at already; it fills p with the problem. Otherwise it
// notes l as the calling port's link.
func takenCaller(callers map[string]Link, mods map[string]Module, l Link, p *Problem) bool {
	if from, ok := end(mods, l.From); !ok || from.kind != kindCalls {
		return false
	}
	first, taken := callers[l.From]
	if !taken {
		callers[l.From] = l
		return false
	}
	*p = Problem{Link: l, Reason: ReasonTaken, Detail: fmt.Sprintf("%s is linked to %s", l.From, first.To)}
	return true
}

// Added reports the problems that adding l to existing causes: those Check
// finds with it and not without it.
func Added(mods map[string]Module, existing []Link, l Link) []Problem {
	before := map[string]bool{}
	for _, p := range Check(mods, existing) {
		before[p.Words()] = true
	}
	var out []Problem
	for _, p := range Check(mods, append(append([]Link{}, existing...), l)) {
		if !before[p.Words()] {
			out = append(out, p)
		}
	}
	return out
}

// Describe is the shape that crosses a link, as a card shows it: the
// event's shape, or `in → out` for a call. "" when an end is not there.
func Describe(mods map[string]Module, l Link) string {
	from, okFrom := end(mods, l.From)
	if _, okTo := end(mods, l.To); !okFrom || !okTo {
		return ""
	}
	switch {
	case from.event != nil:
		return from.event.String()
	case from.call != nil:
		return "in " + from.call.In.String() + " → out " + from.call.Out.String()
	}
	return ""
}

// port is an end resolved: its module and port name, and what it is.
type port struct {
	module, name string
	kind         kind
	event        *runner.Shape
	call         *runner.PortCall
}

type kind string

const (
	kindEmits   kind = "emits"
	kindAccepts kind = "accepts"
	kindServes  kind = "serves"
	kindCalls   kind = "calls"
)

// end finds the end among the modules. A port name is unique across the
// four kinds of a module, so the name alone finds it.
func end(mods map[string]Module, e string) (port, bool) {
	name := portOf(e)
	p := port{module: moduleOf(e), name: name}
	m, ok := mods[p.module]
	if !ok {
		return p, false
	}
	if s, ok := m.Emits[name]; ok {
		p.kind, p.event = kindEmits, &s
	} else if s, ok := m.Accepts[name]; ok {
		p.kind, p.event = kindAccepts, &s
	} else if c, ok := m.Serves[name]; ok {
		p.kind, p.call = kindServes, &c
	} else if c, ok := m.Calls[name]; ok {
		p.kind, p.call = kindCalls, &c
	} else {
		return p, false
	}
	return p, true
}

// checkLink says whether the link is wrong on its own: both ends are
// there, the kinds match and the shapes are equal.
func checkLink(mods map[string]Module, l Link) (Problem, bool) {
	bad := func(r Reason, detail string) (Problem, bool) {
		return Problem{Link: l, Reason: r, Detail: detail}, true
	}
	from, okFrom := end(mods, l.From)
	to, okTo := end(mods, l.To)
	switch {
	case !okFrom:
		return bad(ReasonEnd, missing("from", mods, from))
	case !okTo:
		return bad(ReasonEnd, missing("to", mods, to))
	case (from.kind != kindEmits || to.kind != kindAccepts) && (from.kind != kindCalls || to.kind != kindServes):
		return bad(ReasonKind, fmt.Sprintf("%s is %s, %s is %s; a link joins emits to accepts, or calls to serves", l.From, from.kind, l.To, to.kind))
	}
	if from.event != nil && !from.event.Equal(*to.event) {
		return bad(ReasonShape, fmt.Sprintf("%s carries %s, %s takes %s", l.From, from.event, l.To, to.event))
	}
	if from.call != nil {
		if !from.call.In.Equal(to.call.In) {
			return bad(ReasonShape, fmt.Sprintf("the request is %s at the caller and %s at the server", from.call.In, to.call.In))
		}
		if !from.call.Out.Equal(to.call.Out) {
			return bad(ReasonShape, fmt.Sprintf("the answer is %s at the caller and %s at the server", from.call.Out, to.call.Out))
		}
	}
	return Problem{}, false
}

// missing says which end is not there and why.
func missing(which string, mods map[string]Module, p port) string {
	if _, ok := mods[p.module]; !ok {
		return fmt.Sprintf("%s: the module %s is not installed and running", which, p.module)
	}
	return fmt.Sprintf("%s: the module %s has no port %s", which, p.module, p.name)
}

func moduleOf(e string) string {
	m, _, _ := strings.Cut(e, ".")
	return m
}

func portOf(e string) string {
	_, p, _ := strings.Cut(e, ".")
	return p
}

// Ends are the modules and ports of a link that carries events or calls, by
// short name.
type Ends struct {
	FromModule, FromPort, ToModule, ToPort string
}

// EventEnds are the ends of l when it joins an output to an input; false
// for a link of calls, or one with an end that is not there. It does not
// check the shapes: Check does.
func EventEnds(mods map[string]Module, l Link) (Ends, bool) {
	from, okFrom := end(mods, l.From)
	to, okTo := end(mods, l.To)
	if !okFrom || !okTo || from.kind != kindEmits || to.kind != kindAccepts {
		return Ends{}, false
	}
	return Ends{FromModule: from.module, FromPort: from.name, ToModule: to.module, ToPort: to.name}, true
}

// CallEnds are the ends of l when it joins a calling port to a serving
// port; false for a link of events, or one with an end that is not there.
// It does not check the shapes: Check does. Ends.FromPort is the calling
// port, Ends.ToPort the served one.
func CallEnds(mods map[string]Module, l Link) (Ends, bool) {
	from, okFrom := end(mods, l.From)
	to, okTo := end(mods, l.To)
	if !okFrom || !okTo || from.kind != kindCalls || to.kind != kindServes {
		return Ends{}, false
	}
	return Ends{FromModule: from.module, FromPort: from.name, ToModule: to.module, ToPort: to.name}, true
}
