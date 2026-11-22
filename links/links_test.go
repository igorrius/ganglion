package links_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/runner"
)

var (
	num    = runner.Shape{Type: "number"}
	str    = runner.Shape{Type: "string"}
	reding = runner.Shape{Type: "object", Fields: map[string]runner.Shape{"soc": num}}
)

func call(in, out runner.Shape) runner.PortCall { return runner.PortCall{In: in, Out: out} }

// mod is a module with an output x and an input y of one shape, a calling
// port c and a serving port s, listening to cells.
func mod(listens ...string) links.Module {
	return links.Module{
		Emits:   map[string]runner.Shape{"x": reding},
		Accepts: map[string]runner.Shape{"y": reding, "z": num},
		Serves:  map[string]runner.PortCall{"s": call(num, str)},
		Calls:   map[string]runner.PortCall{"c": call(num, str)},
		Listens: listens,
	}
}

func mods(names ...string) map[string]links.Module {
	m := map[string]links.Module{}
	for _, n := range names {
		m[n] = mod()
	}
	return m
}

func link(from, to string) links.Link { return links.Link{From: from, To: to} }

func TestCheckReportsEachReasonOnItsLink(t *testing.T) {
	ms := mods("a", "b")
	for name, tc := range map[string]struct {
		l    links.Link
		want links.Reason // "" is a good link
	}{
		"event":            {link("a.x", "b.y"), ""},
		"call":             {link("a.c", "b.s"), ""},
		"no module":        {link("a.x", "q.y"), links.ReasonEnd},
		"no port":          {link("a.nope", "b.y"), links.ReasonEnd},
		"input as output":  {link("a.y", "b.y"), links.ReasonKind},
		"output as input":  {link("a.x", "b.x"), links.ReasonKind},
		"emits to serves":  {link("a.x", "b.s"), links.ReasonKind},
		"calls to accepts": {link("a.c", "b.y"), links.ReasonKind},
		"event shape":      {link("a.x", "b.z"), links.ReasonShape},
	} {
		t.Run(name, func(t *testing.T) {
			ps := links.Check(ms, []links.Link{tc.l})
			switch {
			case tc.want == "" && len(ps) != 0:
				t.Fatalf("problems: %+v", ps)
			case tc.want != "" && (len(ps) != 1 || ps[0].Reason != tc.want || ps[0].Link != tc.l):
				t.Fatalf("problems %+v, want one %s on the link", ps, tc.want)
			}
		})
	}
}

func TestCheckCallShapes(t *testing.T) {
	ms := mods("a", "b")
	b := ms["b"]
	b.Serves = map[string]runner.PortCall{"s": call(num, num)}
	ms["b"] = b
	ps := links.Check(ms, []links.Link{link("a.c", "b.s")})
	if len(ps) != 1 || ps[0].Reason != links.ReasonShape || !strings.Contains(ps[0].Detail, "answer") {
		t.Fatalf("problems %+v", ps)
	}
	b.Serves = map[string]runner.PortCall{"s": call(str, str)}
	ms["b"] = b
	ps = links.Check(ms, []links.Link{link("a.c", "b.s")})
	if len(ps) != 1 || !strings.Contains(ps[0].Detail, "request") {
		t.Fatalf("problems %+v", ps)
	}
}

func loops(ps []links.Problem) [][]string {
	var out [][]string
	for _, p := range ps {
		if p.Reason == links.ReasonLoop {
			out = append(out, p.Path)
		}
	}
	return out
}

func TestCheckLoops(t *testing.T) {
	for name, tc := range map[string]struct {
		mods  map[string]links.Module
		links []links.Link
		want  []string // nil: no loop
	}{
		"links two": {mods("a", "b"), []links.Link{link("a.x", "b.y"), link("b.x", "a.y")}, []string{"a", "b", "a"}},
		"links three": {mods("a", "b", "c"),
			[]links.Link{link("a.x", "b.y"), link("b.x", "c.y"), link("c.x", "a.y")}, []string{"a", "b", "c", "a"}},
		"a call closes it": {mods("a", "b"), []links.Link{link("a.x", "b.y"), link("b.c", "a.s")}, []string{"a", "b", "a"}},
		"self":             {mods("a"), []links.Link{link("a.x", "a.y")}, []string{"a", "a"}},
		"cells two":        {map[string]links.Module{"a": mod("b.p"), "b": mod("a.p")}, nil, []string{"a", "b", "a"}},
		"link and cell": {map[string]links.Module{"a": mod(), "b": mod("a.p")},
			[]links.Link{link("b.x", "a.y")}, []string{"a", "b", "a"}},
		"a fan is fine": {mods("a", "b", "c"), []links.Link{link("a.x", "b.y"), link("a.x", "c.y")}, nil},
		"a diamond is fine": {mods("a", "b", "c", "d"),
			[]links.Link{link("a.x", "b.y"), link("a.x", "c.y"), link("b.x", "d.y"), link("c.x", "d.y")}, nil},
		"prefix is whole":        {map[string]links.Module{"a": mod("ab.p"), "ab": mod(), "b": mod("a.p")}, nil, nil},
		"a bad link is left out": {mods("a", "b"), []links.Link{link("a.x", "b.y"), link("b.x", "a.z")}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := loops(links.Check(tc.mods, tc.links))
			if tc.want == nil && len(got) != 0 || tc.want != nil && (len(got) != 1 || !slices.Equal(got[0], tc.want)) {
				t.Fatalf("loops %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoopBlamesTheFirstLinkOnThePath(t *testing.T) {
	ms := map[string]links.Module{"a": mod("b.p"), "b": mod()}
	ps := links.Check(ms, []links.Link{link("a.x", "b.y")}) // b -> a by cell, a -> b by link
	if len(ps) != 1 || ps[0].Link != link("a.x", "b.y") {
		t.Fatalf("problems %+v", ps)
	}
	ps = links.Check(map[string]links.Module{"a": mod("b.p"), "b": mod("a.p")}, nil)
	if len(ps) != 1 || ps[0].Link != (links.Link{}) {
		t.Fatalf("cells alone: %+v", ps)
	}
}

func TestCheckDepth(t *testing.T) {
	chain := func(n int) ([]string, []links.Link) {
		names := []string{"a", "b", "c", "d", "e", "f"}[:n]
		var ls []links.Link
		for i := 0; i+1 < n; i++ {
			ls = append(ls, link(names[i]+".x", names[i+1]+".y"))
		}
		return names, ls
	}
	names, ls := chain(4)
	if ps := links.Check(mods(names...), ls); len(ps) != 0 {
		t.Fatalf("four modules are allowed: %+v", ps)
	}
	names, ls = chain(5)
	ps := links.Check(mods(names...), ls)
	if len(ps) != 1 || ps[0].Reason != links.ReasonDeep || !slices.Equal(ps[0].Path, names) || ps[0].Link != ls[0] {
		t.Fatalf("five modules: %+v", ps)
	}
	// the chain runs through cells and links together
	ms := mods("a", "b", "c", "d")
	ms["c"] = mod("b.p")
	ms["d"] = mod("c.p")
	ms["b"] = mod("a.p")
	ps = links.Check(ms, []links.Link{link("d.x", "e.y")})
	if len(ps) != 1 || ps[0].Reason != links.ReasonEnd {
		t.Fatalf("a link to nowhere: %+v", ps)
	}
	ms["e"] = mod()
	ps = links.Check(ms, []links.Link{link("d.x", "e.y")})
	if len(ps) != 1 || ps[0].Reason != links.ReasonDeep || !slices.Equal(ps[0].Path, []string{"a", "b", "c", "d", "e"}) {
		t.Fatalf("cells and a link: %+v", ps)
	}
}

func TestAddedReportsOnlyWhatTheLinkCauses(t *testing.T) {
	ms := mods("a", "b", "c")
	existing := []links.Link{link("a.x", "b.y"), link("a.x", "q.y")} // the second is broken already
	if ps := links.Added(ms, existing, link("a.x", "c.y")); len(ps) != 0 {
		t.Fatalf("a good link: %+v", ps)
	}
	ps := links.Added(ms, existing, link("b.x", "a.y"))
	if len(ps) != 1 || ps[0].Reason != links.ReasonLoop {
		t.Fatalf("a loop: %+v", ps)
	}
}

func TestInvolvesAndWords(t *testing.T) {
	ps := links.Check(mods("a", "b"), []links.Link{link("a.x", "b.y"), link("b.x", "a.y"), link("a.x", "q.y")})
	for _, p := range ps {
		if !p.Involves("a") || p.Involves("zz") || p.Words() == "" {
			t.Errorf("problem %+v", p)
		}
	}
	if w := links.Check(mods("a"), []links.Link{link("a.x", "q.y")})[0].Words(); w != "a.x → q.y: to: the module q is not installed and running" {
		t.Errorf("words %q", w)
	}
}

func TestDescribe(t *testing.T) {
	ms := mods("a", "b")
	if got := links.Describe(ms, link("a.x", "b.y")); got != "{soc: number}" {
		t.Errorf("event: %q", got)
	}
	if got := links.Describe(ms, link("a.c", "b.s")); got != "in number → out string" {
		t.Errorf("call: %q", got)
	}
	if got := links.Describe(ms, link("a.x", "q.y")); got != "" {
		t.Errorf("missing: %q", got)
	}
}

func TestNotifyOutputIsAnEnd(t *testing.T) {
	m := runner.Manifest{Capabilities: runner.Capabilities{Notify: &runner.Notify{PerDay: 1}}}
	a := links.FromManifest(m)
	b := links.Module{Accepts: map[string]runner.Shape{"notices": a.Emits[runner.NotifyOutput]}}
	ms := map[string]links.Module{"a": a, "b": b}
	if ps := links.Check(ms, []links.Link{link("a.notify", "b.notices")}); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
}

// A calling port has at most one link: the second line from it is the
// problem, the first keeps working.
func TestACallingPortHasOneLink(t *testing.T) {
	ms := mods("a", "b", "c")
	ls := []links.Link{link("a.c", "b.s"), link("a.c", "c.s"), link("b.c", "c.s")}
	ps := links.Check(ms, ls)
	if len(ps) != 1 || ps[0].Reason != links.ReasonTaken || ps[0].Link != ls[1] {
		t.Fatalf("problems %+v, want one taken on the second link", ps)
	}
	if got := ps[0].Words(); !strings.Contains(got, "the calling port is linked already") || !strings.Contains(got, "b.s") {
		t.Fatalf("words %q", got)
	}
	if ps := links.Added(ms, ls[:1], ls[1]); len(ps) != 1 || ps[0].Reason != links.ReasonTaken {
		t.Fatalf("added problems %+v", ps)
	}
	// The same emits port may feed many inputs.
	if ps := links.Check(ms, []links.Link{link("a.x", "b.y"), link("a.x", "c.y")}); len(ps) != 0 {
		t.Fatalf("problems %+v", ps)
	}
}

func TestCallEnds(t *testing.T) {
	ms := mods("a", "b")
	got, ok := links.CallEnds(ms, link("a.c", "b.s"))
	if want := (links.Ends{FromModule: "a", FromPort: "c", ToModule: "b", ToPort: "s"}); !ok || got != want {
		t.Fatalf("CallEnds = %+v, %v", got, ok)
	}
	for name, l := range map[string]links.Link{
		"an event":   link("a.x", "b.y"),
		"no module":  link("a.c", "q.s"),
		"no port":    link("a.c", "b.nope"),
		"wrong kind": link("a.c", "b.y"),
	} {
		if _, ok := links.CallEnds(ms, l); ok {
			t.Errorf("%s: CallEnds says it is a call", name)
		}
	}
	if _, ok := links.EventEnds(ms, link("a.c", "b.s")); ok {
		t.Error("EventEnds takes a call")
	}
}
