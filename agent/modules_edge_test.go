package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modstore"
)

// edgeLocal lists modules that run on edges.
type edgeLocal struct {
	*fakeLocal
	list []modstore.LocalModule
}

func (l edgeLocal) Local(context.Context) ([]modstore.LocalModule, error) { return l.list, nil }

// A module on an edge shows what the edge says it runs, with the edge's
// words escaped (R-EDGE-6).
func TestModulesListShowsModulesOnAnEdge(t *testing.T) {
	e, l, _ := ownModulesEnv(t)
	edge := func(r modstore.EdgeRun) modstore.LocalModule {
		r.Node = "home"
		return modstore.LocalModule{Name: "demo-reader", State: modstore.RunEdge, Version: "5", Edge: &r}
	}
	for name, c := range map[string]struct {
		m    modstore.LocalModule
		want string
	}{
		"runs":    {edge(modstore.EdgeRun{Result: modstore.EdgeRunning, Version: "4"}), "- demo-reader: on the edge home: it runs version 4"},
		"refused": {edge(modstore.EdgeRun{Result: modstore.EdgeRefused, Version: "4", Reason: "a_b failed"}), "- demo-reader: on the edge home: the edge refused version 5 (a\\_b failed); it runs version 4"},
		"nothing": {edge(modstore.EdgeRun{Result: modstore.EdgeRefused, Reason: "bad"}), "- demo-reader: on the edge home: the edge refused version 5 (bad); nothing runs"},
		"waiting": {edge(modstore.EdgeRun{}), "- demo-reader: on the edge home: the edge has not taken version 5 yet"},
	} {
		e.deps.Local = edgeLocal{fakeLocal: l, list: []modstore.LocalModule{c.m}}
		e.a = New(overDefaults(Config{}), e.deps)
		got := oneReply(t, e, "/modules")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s:\n%s\nwant line %q", name, got, c.want)
		}
	}
}
