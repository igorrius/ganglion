package links

import (
	"slices"
	"sort"
	"strings"
)

// edge runs from the module that starts to the module that is started: a
// producer to its receiver, a caller to its server, a cell's owner to its
// listener.
type edge struct{ from, to string }

// graph is the edges among the modules, each with the first link that
// makes it (the zero Link for a cell trigger), and the sorted neighbours.
type graph struct {
	via   map[edge]Link
	next  map[string][]string
	names []string
}

// newGraph builds the graph of the cell triggers of the modules and the
// links, which are already known to be good.
func newGraph(mods map[string]Module, ls []Link) graph {
	g := graph{via: map[edge]Link{}, next: map[string][]string{}}
	add := func(e edge, l Link) {
		if _, ok := g.via[e]; !ok {
			g.via[e] = l
			g.next[e.from] = append(g.next[e.from], e.to)
		}
	}
	for _, name := range sortedNames(mods) {
		for _, id := range mods[name].Listens {
			if owner, _, ok := strings.Cut(id, "."); ok {
				if _, active := mods[owner]; active {
					add(edge{owner, name}, Link{})
				}
			}
		}
	}
	for _, l := range ls {
		add(edge{moduleOf(l.From), moduleOf(l.To)}, l)
	}
	for _, to := range g.next {
		sort.Strings(to)
	}
	g.names = sortedNames(mods)
	return g
}

func sortedNames(mods map[string]Module) []string {
	names := make([]string, 0, len(mods))
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// linkOn is the first link along the path, or the zero Link when every step
// is a cell trigger.
func (g graph) linkOn(path []string) Link {
	for i := 0; i+1 < len(path); i++ {
		if l := g.via[edge{path[i], path[i+1]}]; l != (Link{}) {
			return l
		}
	}
	return Link{}
}

// checkGraph reports the cycles of the graph, one for each module a cycle
// has not covered yet; with no cycle, the longest chain when it is deeper
// than MaxDepth.
func checkGraph(mods map[string]Module, good []Link) []Problem {
	g := newGraph(mods, good)
	var out []Problem
	covered := map[string]bool{}
	for _, n := range g.names {
		if covered[n] {
			continue
		}
		if cycle := g.cycleThrough(n); cycle != nil {
			for _, m := range cycle {
				covered[m] = true
			}
			out = append(out, Problem{Link: g.linkOn(cycle), Reason: ReasonLoop, Path: cycle})
		}
	}
	if len(out) > 0 {
		return out // the depth of a graph with a cycle is not defined
	}
	if chain := g.longest(); len(chain) > MaxDepth {
		return []Problem{{Link: g.linkOn(chain), Reason: ReasonDeep, Path: chain}}
	}
	return nil
}

// cycleThrough is a cycle that starts and ends at start (["a","b","a"]),
// nil when none passes through it.
func (g graph) cycleThrough(start string) []string {
	path, seen := []string{start}, map[string]bool{}
	var walk func(n string) bool
	walk = func(n string) bool {
		for _, m := range g.next[n] {
			if m == start {
				path = append(path, m)
				return true
			}
			if seen[m] {
				continue
			}
			seen[m] = true
			path = append(path, m)
			if walk(m) {
				return true
			}
			path = path[:len(path)-1]
		}
		return false
	}
	if walk(start) {
		return path
	}
	return nil
}

// longest is the longest path of the graph, which has no cycle; the first
// in name order among equals.
func (g graph) longest() []string {
	memo := map[string][]string{}
	var from func(n string) []string
	from = func(n string) []string {
		if p, ok := memo[n]; ok {
			return p
		}
		best := []string{}
		for _, m := range g.next[n] {
			if p := from(m); len(p) > len(best) {
				best = p
			}
		}
		memo[n] = append([]string{n}, best...)
		return memo[n]
	}
	var best []string
	for _, n := range g.names {
		if p := from(n); len(p) > len(best) {
			best = p
		}
	}
	return slices.Clone(best)
}
