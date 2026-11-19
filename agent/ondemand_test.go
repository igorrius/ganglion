package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
)

// onDemandTool is a tool a session must turn on; use is the tool that does it.
type onDemandTool struct {
	fakeTool
}

func (o *onDemandTool) Spec() tool.Spec {
	s := o.fakeTool.Spec()
	s.OnDemand = true
	return s
}

// newUse is a stand-in for mcp.use: it turns "shop.search" on.
func newUse() *fakeTool {
	return &fakeTool{name: "mcp.use", result: tool.Result{Text: "on from the next step: shop.search", On: []string{"shop.search"}}}
}

func offeredNames(req provider.ChatRequest) []string {
	var out []string
	for _, tl := range req.Tools {
		out = append(out, tl.Name)
	}
	slices.Sort(out)
	return out
}

// A tool turned on joins the next step's tool list, stays for the session's
// next turns, runs there, and is gone with a new session (R-MCP-4a).
func TestOnDemandToolJoinsTheSession(t *testing.T) {
	use := newUse()
	search := &onDemandTool{fakeTool{name: "shop.search", result: tool.Result{Text: "found"}}}
	e := newEnv(t, Config{}, registry(t, use, search))
	e.smart.push(callTool("mcp.use"), callTool("shop.search"), say("done"), say("again"))

	e.turn(t, message("telegram:user:42", "Igor", "find"))
	reqs := e.smart.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d model calls, want 3", len(reqs))
	}
	if got := offeredNames(reqs[0]); !slices.Equal(got, []string{"mcp.use"}) {
		t.Fatalf("step 1 offered %v, want only mcp.use", got)
	}
	if got := offeredNames(reqs[1]); !slices.Equal(got, []string{"mcp.use", "shop.search"}) {
		t.Fatalf("step 2 offered %v, want the tool turned on too", got)
	}
	if len(search.calls) != 1 || !slices.Equal(search.calls[0].On, []string{"shop.search"}) {
		t.Fatalf("the on-demand tool's calls = %+v", search.calls)
	}
	if st := e.state(t); !slices.Equal(st.Tools, []string{"shop.search"}) {
		t.Fatalf("state tools = %v", st.Tools)
	}

	e.turn(t, message("telegram:user:42", "Igor", "more"))
	reqs = e.smart.requests()
	if got := offeredNames(reqs[len(reqs)-1]); !slices.Equal(got, []string{"mcp.use", "shop.search"}) {
		t.Fatalf("the next turn offered %v: the tool stays on", got)
	}

	e.turn(t, message("telegram:user:42", "Igor", "/new"))
	if st := e.state(t); len(st.Tools) != 0 {
		t.Fatalf("a new session kept %v", st.Tools)
	}
}

// A turn that turns the same tool on twice lists it once.
func TestOnDemandToolsAreNotDuplicated(t *testing.T) {
	use := newUse()
	e := newEnv(t, Config{}, registry(t, use))
	e.smart.push(callTools("mcp.use", "mcp.use"), say("ok"))
	e.turn(t, message("telegram:user:42", "Igor", "go"))
	if st := e.state(t); !slices.Equal(st.Tools, []string{"shop.search"}) {
		t.Fatalf("state tools = %v", st.Tools)
	}
}

// A model that calls an on-demand tool it was not offered is refused by the
// registry, and the tool does not run.
func TestOnDemandToolRefusedWhenNotOn(t *testing.T) {
	search := &onDemandTool{fakeTool{name: "shop.search", result: tool.Result{Text: "found"}}}
	e := newEnv(t, Config{}, registry(t, newUse(), search))
	e.smart.push(callTool("shop.search"), say("ok"))
	e.turn(t, message("telegram:user:42", "Igor", "go"))
	if len(search.calls) != 0 {
		t.Fatalf("a tool that was not turned on ran: %+v", search.calls)
	}
}

// State carries the turned-on tools through its wire form.
func TestStateRoundTripsTools(t *testing.T) {
	raw, err := json.Marshal(State{V: StateVersion, Tools: []string{"a.b", "c.d"}})
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeState(raw)
	if err != nil || !slices.Equal(back.Tools, []string{"a.b", "c.d"}) {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	raw, _ = json.Marshal(State{V: StateVersion})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["tools"]; ok {
		t.Fatalf("an empty list is written: %s", raw)
	}
}

// Abuse: a tool mcp.use turns on is not callable later in the same step,
// in the same answer of an API model (R-MCP-4a: from the next step).
func TestOnDemandToolNotCallableInTheSameStep(t *testing.T) {
	search := &onDemandTool{fakeTool{name: "shop.search", result: tool.Result{Text: "found"}}}
	e := newEnv(t, Config{}, registry(t, newUse(), search))
	e.smart.push(callTools("mcp.use", "shop.search"), say("ok"))
	e.turn(t, message("telegram:user:42", "Igor", "go"))
	if len(search.calls) != 0 {
		t.Fatalf("a tool turned on in this step ran in it: %+v", search.calls)
	}
	if st := e.state(t); !slices.Equal(st.Tools, []string{"shop.search"}) {
		t.Fatalf("state tools = %v", st.Tools)
	}
}

// A CLI provider runs its whole tool loop in one step, and its model takes
// a step after every call: a tool mcp.use turned on reaches the run's tool
// list, and the CLI's next step calls it, in the same turn (R-MCP-4a,
// #284). Before mcp.use the CLI does not have it.
func TestOnDemandToolCallableInTheSameCLIRun(t *testing.T) {
	search := &onDemandTool{fakeTool{name: "shop.search", result: tool.Result{Text: "found"}}}
	use := newUse()
	e := newEnv(t, Config{}, registry(t, use, search))
	e.smart.push(likeCLI("shop_search", "mcp_use", "shop_search"))
	out := e.turn(t, message("telegram:user:42", "Igor", "go"))
	if len(use.calls) != 1 || len(search.calls) != 1 {
		t.Fatalf("mcp.use ran %d times and shop.search %d, want 1 and 1", len(use.calls), len(search.calls))
	}
	if !slices.Equal(search.calls[0].On, []string{"shop.search"}) {
		t.Fatalf("shop.search ran with On = %v", search.calls[0].On)
	}
	got := results(t, reply(t, out))
	if !got[0].IsError || got[2].IsError || got[2].Content[0].Text != "found" {
		t.Fatalf("results %+v", got)
	}
	if n := len(e.smart.requests()); n != 1 {
		t.Fatalf("%d model calls, want the one CLI run", n)
	}
	var ran []string
	for _, m := range e.state(t).Messages {
		for _, c := range m.ToolCalls {
			ran = append(ran, c.Name)
		}
	}
	if !slices.Equal(ran, []string{"mcp.use", "shop.search"}) {
		t.Fatalf("the history holds the calls %v", ran)
	}
}

// Abuse: a name the CLI run was not offered reaches the agent all the
// same, and an on-demand tool no call turned on stays refused.
func TestOnDemandToolNotOnRefusedInACLIRun(t *testing.T) {
	search := &onDemandTool{fakeTool{name: "shop.search", result: tool.Result{Text: "found"}}}
	other := &onDemandTool{fakeTool{name: "shop.buy", result: tool.Result{Text: "bought"}}}
	use := newUse()
	e := newEnv(t, Config{}, registry(t, use, search, other))
	e.smart.push(viaMCP("shop_search", "mcp_use", "shop_buy"))
	e.turn(t, message("telegram:user:42", "Igor", "go"))
	if len(use.calls) != 1 {
		t.Fatalf("mcp.use ran %d times over the bridge, want 1", len(use.calls))
	}
	if len(search.calls) != 0 || len(other.calls) != 0 {
		t.Fatalf("a tool that was not on ran: %+v %+v", search.calls, other.calls)
	}
}
