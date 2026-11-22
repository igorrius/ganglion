package builtin_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// linkFake is the links of the node's file.
type linkFake struct{ ls []settings.Link }

func (f *linkFake) Links() ([]settings.Link, error) { return slices.Clone(f.ls), nil }
func (f *linkFake) AddLink(l settings.Link) error   { f.ls = append(f.ls, l); return nil }
func (f *linkFake) RemoveLink(l settings.Link) error {
	f.ls = slices.DeleteFunc(f.ls, func(x settings.Link) bool { return x == l })
	return nil
}

func linkMods() map[string]links.Module {
	num := runner.Shape{Type: "number"}
	str := runner.Shape{Type: "string"}
	reading := runner.Shape{Type: "object", Fields: map[string]runner.Shape{"soc": num, "load": num}}
	mod := func(listens ...string) links.Module {
		return links.Module{
			Emits:   map[string]runner.Shape{"reading": reading},
			Accepts: map[string]runner.Shape{"reading_in": reading, "text": str},
			Serves:  map[string]runner.PortCall{"history": {In: num, Out: str}},
			Listens: listens,
		}
	}
	return map[string]links.Module{"reader": mod(), "alerts": mod()}
}

func newLinking(f *linkFake, mods map[string]links.Module) map[string]tool.Handler {
	l := builtin.Linking{File: f, Modules: func(context.Context) (map[string]links.Module, error) { return mods, nil }}
	out := map[string]tool.Handler{}
	for _, h := range l.Tools() {
		out[h.Spec().Name] = h
	}
	return out
}

func TestLinkToolsAreTheAdminsAndConfirmed(t *testing.T) {
	tools := newLinking(&linkFake{}, linkMods())
	if len(tools) != 3 {
		t.Fatalf("%d tools", len(tools))
	}
	for name, h := range tools {
		s := h.Spec()
		if !s.AdminOnly {
			t.Errorf("%s is offered to everyone", name)
		}
		if name != "link.list" && s.Policy != tool.PolicyConfirm {
			t.Errorf("%s changes the links without asking", name)
		}
		if err := tool.CheckArgs(s, []byte(`{"unknown":1}`)); err == nil {
			t.Errorf("%s takes unknown arguments", name)
		}
	}
}

func TestLinkAddShowsBothEndsAndTheShapeThenWrites(t *testing.T) {
	f := &linkFake{}
	add := newLinking(f, linkMods())["link.add"]
	c := routeCall("link.add", `{"from":"reader.reading","to":"alerts.reading_in"}`)
	q, err := ask(t, add, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`reader.reading`, `alerts.reading_in`, `{load: number, soc: number}`} {
		if !strings.Contains(q, strings.ReplaceAll(want, "_", `\_`)) && !strings.Contains(q, want) {
			t.Errorf("the card lacks %q: %q", want, q)
		}
	}
	if len(f.ls) != 0 {
		t.Fatal("the question wrote the link")
	}
	if _, err := add.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(f.ls) != 1 || f.ls[0] != (settings.Link{From: "reader.reading", To: "alerts.reading_in"}) {
		t.Fatalf("links %+v", f.ls)
	}
	// the same link again is no new link
	if _, err := ask(t, add, c); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("a second add: %v", err)
	}
}

// A refused link answers with the node's reason and opens no card.
func TestLinkAddRefusesWithTheNodesReason(t *testing.T) {
	for name, tc := range map[string]struct{ args, want string }{
		"no module": {`{"from":"reader.reading","to":"ghost.reading_in"}`, "ghost is not installed"},
		"no port":   {`{"from":"reader.nope","to":"alerts.reading_in"}`, "has no port nope"},
		"kind":      {`{"from":"reader.reading","to":"alerts.history"}`, "kinds do not match"},
		"shape":     {`{"from":"reader.reading","to":"alerts.text"}`, "shapes differ"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &linkFake{}
			add := newLinking(f, linkMods())["link.add"]
			_, err := ask(t, add, routeCall("link.add", tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
			if _, err := add.Run(context.Background(), routeCall("link.add", tc.args)); err == nil || len(f.ls) != 0 {
				t.Fatalf("the press wrote %+v, err %v", f.ls, err)
			}
		})
	}
}

func TestLinkAddRefusesALoopAtThePressToo(t *testing.T) {
	f := &linkFake{ls: []settings.Link{{From: "reader.reading", To: "alerts.reading_in"}}}
	mods := linkMods()
	add := newLinking(f, mods)["link.add"]
	c := routeCall("link.add", `{"from":"alerts.reading","to":"reader.reading_in"}`)
	if _, err := ask(t, add, c); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("question: %v", err)
	}
	if _, err := add.Run(context.Background(), c); err == nil || len(f.ls) != 1 {
		t.Fatalf("run: %v, links %+v", err, f.ls)
	}
}

func TestLinkRemoveAndList(t *testing.T) {
	f := &linkFake{ls: []settings.Link{{From: "reader.reading", To: "alerts.reading_in"}, {From: "reader.reading", To: "gone.reading_in"}}}
	tools := newLinking(f, linkMods())
	res, err := tools["link.list"].Run(context.Background(), call("link.list", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"emits reader.reading: {load: number, soc: number}", "serves alerts.history: in number, out string",
		"- reader.reading → alerts.reading_in\n", "- reader.reading → gone.reading_in (does not work: to: the module gone is not installed and running)"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the list lacks %q:\n%s", want, res.Text)
		}
	}
	rm := tools["link.remove"]
	c := routeCall("link.remove", `{"from":"reader.reading","to":"gone.reading_in"}`)
	if q, err := ask(t, rm, c); err != nil || !strings.Contains(q, "gone.reading") {
		t.Fatalf("question %q, %v", q, err)
	}
	if _, err := rm.Run(context.Background(), c); err != nil || len(f.ls) != 1 {
		t.Fatalf("links %+v, %v", f.ls, err)
	}
	if _, err := ask(t, rm, c); err == nil {
		t.Fatal("removing a link that is not there")
	}
}

func TestLinkToolsRefuseAScheduledRun(t *testing.T) {
	add := newLinking(&linkFake{}, linkMods())["link.add"]
	c := routeCall("link.add", `{"from":"reader.reading","to":"alerts.reading_in"}`)
	c.Grant = &tool.Grant{}
	if _, err := add.Run(context.Background(), c); err == nil {
		t.Fatal("a scheduled run changed the links")
	}
}
