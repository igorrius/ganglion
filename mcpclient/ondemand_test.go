package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/mcpclient/mcpclienttest"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
)

// shopRig is a connected hub with a server called shop.
func shopRig(t *testing.T, raws []json.RawMessage, policies map[string]tool.Policy) *rig {
	t.Helper()
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: raws})
	r := newRig(t)
	r.hub.Set([]mcpclient.Server{config("shop", srv, raws, policies)})
	r.await(t, "shop", mcpclient.StateConnected)
	return r
}

func meta(t *testing.T, hub *mcpclient.Hub, name string) tool.Handler {
	t.Helper()
	for _, h := range hub.Tools() {
		if h.Spec().Name == name {
			return h
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

func run(h tool.Handler, on []string, args string) (tool.Result, error) {
	return h.Run(context.Background(), tool.Call{Name: h.Spec().Name, Admin: true, On: on, TurnedOn: on, Args: json.RawMessage(args)})
}

func TestMetaToolsAreLocalSafeAndGated(t *testing.T) {
	hub := mcpclient.NewHub()
	for _, h := range hub.Tools() {
		s := h.Spec()
		if !s.AdminOnly || !s.LocalSafe || s.Class != tool.ClassRead || s.Policy != tool.PolicyAuto || s.OnDemand {
			t.Errorf("%s: %+v", s.Name, s)
		}
		if h.(tool.Gated).Offered() {
			t.Errorf("%s is on offer with no server set", s.Name)
		}
		if _, err := run(h, nil, `{}`); err == nil {
			t.Errorf("%s ran with no server set", s.Name)
		}
	}
	hub.Set([]mcpclient.Server{{Name: "shop", URL: "http://shop.invalid"}})
	for _, h := range hub.Tools() {
		if !h.(tool.Gated).Offered() {
			t.Errorf("%s is not on offer with a server set", h.Spec().Name)
		}
	}
}

func TestMcpToolsListsTheServersAndTheirTools(t *testing.T) {
	raws := []json.RawMessage{
		mcpclienttest.Tool("search", "Search the shop.\nSecond line that is not shown.", "", true),
		mcpclienttest.Tool("secret", "Hidden thing.", "", true),
		mcpclienttest.Tool("buy", strings.Repeat("long ", 100), "", false),
	}
	r := shopRig(t, raws, map[string]tool.Policy{"search": tool.PolicyAuto, "secret": tool.PolicyForbidden, "buy": tool.PolicyConfirm})
	h := meta(t, r.hub, "mcp.tools")

	res, err := run(h, nil, `{}`)
	if err != nil || res.External || res.Text != "shop: connected, 3 tools" {
		t.Fatalf("servers = %+v, %v", res, err)
	}
	res, err = run(h, nil, `{"server":"shop"}`)
	if err != nil || res.External {
		t.Fatalf("tools = %+v, %v", res, err)
	}
	lines := strings.Split(res.Text, "\n")
	if len(lines) != 3 || lines[1] != "shop.search [auto]: Search the shop." ||
		!strings.HasPrefix(lines[2], "shop.secret [forbidden]") || strings.Contains(lines[2], "Hidden") ||
		!strings.HasPrefix(lines[0], "shop.buy [confirm]: long long") || len([]rune(lines[0])) > 260 {
		t.Fatalf("lines = %q", lines)
	}
	if _, err := run(h, nil, `{"server":"nope"}`); err == nil || !strings.Contains(err.Error(), "shop") {
		t.Errorf("an unknown server: %v", err)
	}
	if _, err := run(h, nil, `{"srv":"shop"}`); !errors.Is(err, tool.ErrBadArgs) {
		t.Errorf("an undeclared argument: %v", err)
	}
}

func TestMcpToolsOfAServerThatIsNotConnected(t *testing.T) {
	hub := mcpclient.NewHub()
	hub.Set([]mcpclient.Server{{Name: "shop", URL: "http://shop.invalid"}})
	res, err := run(meta(t, hub, "mcp.tools"), nil, `{"server":"shop"}`)
	if err != nil || !strings.Contains(res.Text, "connecting") || strings.Contains(res.Text, "shop.") {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestMcpUseTurnsToolsOn(t *testing.T) {
	raws := []json.RawMessage{
		mcpclienttest.Tool("search", "Search.", "", true),
		mcpclienttest.Tool("shop_buy", "Buy.", "", false),
		mcpclienttest.Tool("Secret-Thing", "Off.", "", true),
	}
	r := shopRig(t, raws, map[string]tool.Policy{"search": tool.PolicyAuto, "buy": tool.PolicyConfirm, "secret_thing": tool.PolicyForbidden})
	use := meta(t, r.hub, "mcp.use")

	res, err := run(use, nil, `{"server":"shop","tools":["search","shop.buy"]}`)
	if err != nil || !strings.HasPrefix(res.Text, "on from the next step: shop.search, shop.buy\n\n") || !slices.Equal(res.On, []string{"shop.search", "shop.buy"}) || res.External {
		t.Fatalf("use = %+v, %v", res, err)
	}
	// The server's own name and the full name are the same tool; one that is
	// on already is not an error.
	res, err = run(use, []string{"shop.search"}, `{"server":"shop","tools":["shop_buy","shop.search"]}`)
	if err != nil || res.Text != "on from the next step: shop.buy; already on: shop.search" || res.External {
		t.Fatalf("use again = %+v, %v", res, err)
	}
	for name, args := range map[string]string{
		"a forbidden tool":  `{"server":"shop","tools":["secret_thing"]}`,
		"an unknown tool":   `{"server":"shop","tools":["search","nothing"]}`,
		"an unknown server": `{"server":"other","tools":["search"]}`,
		"no tools":          `{"server":"shop","tools":[]}`,
		"no server":         `{"tools":["search"]}`,
		"too many names":    `{"server":"shop","tools":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]}`,
		"a foreign key":     `{"server":"shop","tools":["search"],"all":true}`,
	} {
		if res, err := run(use, nil, args); err == nil || len(res.On) != 0 {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
	}
}

// At most 16 tools and 48 KB of definitions are on at once; past either the
// call is refused with the limit and nothing is turned on.
func TestMcpUseLimits(t *testing.T) {
	var raws []json.RawMessage
	policies := map[string]tool.Policy{}
	var all []string
	for i := range 20 {
		name := fmt.Sprintf("t%d", i)
		raws = append(raws, mcpclienttest.Tool(name, "d", "", true))
		policies[name] = tool.PolicyAuto
		all = append(all, fmt.Sprintf("shop.%s", name))
	}
	r := shopRig(t, raws, policies)
	use := meta(t, r.hub, "mcp.use")

	on := all[:15]
	if _, err := run(use, on, `{"server":"shop","tools":["t15"]}`); err != nil {
		t.Fatalf("the 16th tool: %v", err)
	}
	res, err := run(use, on, `{"server":"shop","tools":["t15","t16"]}`)
	if err == nil || !strings.Contains(err.Error(), "at most 16") || len(res.On) != 0 {
		t.Fatalf("the 17th tool: %+v, %v", res, err)
	}
	// Already-on names count once.
	if _, err := run(use, all[:16], `{"server":"shop","tools":["t0","t1"]}`); err != nil {
		t.Fatalf("tools that are on already: %v", err)
	}
}

func TestMcpUseByteLimit(t *testing.T) {
	var raws []json.RawMessage
	policies := map[string]tool.Policy{}
	for i := range 8 {
		name := fmt.Sprintf("t%d", i)
		raws = append(raws, mcpclienttest.Tool(name, strings.Repeat("d", 7000), "", true))
		policies[name] = tool.PolicyAuto
	}
	r := shopRig(t, raws, policies)
	use := meta(t, r.hub, "mcp.use")
	if _, err := run(use, nil, `{"server":"shop","tools":["t0","t1","t2","t3","t4","t5"]}`); err != nil {
		t.Fatalf("under the limit: %v", err)
	}
	res, err := run(use, []string{"shop.t0", "shop.t1", "shop.t2", "shop.t3", "shop.t4", "shop.t5"}, `{"server":"shop","tools":["t6","t7"]}`)
	if err == nil || !strings.Contains(err.Error(), "bytes of tool definitions") || len(res.On) != 0 {
		t.Fatalf("over the limit: %+v, %v", res, err)
	}
}

func TestMcpUseRefusesAServerThatIsNotConnected(t *testing.T) {
	hub := mcpclient.NewHub()
	hub.Set([]mcpclient.Server{{Name: "shop", URL: "http://shop.invalid"}})
	if _, err := run(meta(t, hub, "mcp.use"), nil, `{"server":"shop","tools":["search"]}`); err == nil || !strings.Contains(err.Error(), "connecting") {
		t.Fatalf("err = %v", err)
	}
}

// Through the registry as the node builds it: the server's tools are not on
// offer until a session turns them on, mcp.tools and mcp.use always are,
// and a call of a tool that is not on is refused before it reaches the
// server.
func TestOnDemandThroughTheRegistry(t *testing.T) {
	raws := []json.RawMessage{mcpclienttest.Tool("search", "Search.", "", true)}
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: raws})
	r := newRig(t)
	r.hub.Set([]mcpclient.Server{config("shop", srv, raws, approve(tool.PolicyAuto, "search"))})
	r.await(t, "shop", mcpclient.StateConnected)
	reg, err := tool.NewRegistryWith(router.Local{}, []tool.RegistryOption{tool.WithDynamic(tool.Dynamics(r.hub.Handlers))}, r.hub.Tools()...)
	if err != nil {
		t.Fatal(err)
	}
	if got := specNames(reg.Specs(true)); !slices.Equal(got, []string{"mcp.tools", "mcp.use"}) {
		t.Fatalf("specs = %v", got)
	}
	if got := specNames(reg.Specs(false)); len(got) != 0 {
		t.Fatalf("a non-admin is offered %v", got)
	}
	if got := specNames(reg.SessionSpecs(true, []string{"shop.search"})); !slices.Equal(got, []string{"mcp.tools", "mcp.use", "shop.search"}) {
		t.Fatalf("session specs = %v", got)
	}
	if _, err := reg.Run(context.Background(), tool.Call{Name: "shop.search", Admin: true, Args: json.RawMessage(`{}`)}); !errors.Is(err, tool.ErrUnknown) {
		t.Fatalf("a tool that is not on: %v", err)
	}
	if srv.Count("tools/call") != 0 {
		t.Fatal("a call that was not turned on reached the server")
	}
	res, err := reg.Run(context.Background(), tool.Call{Name: "shop.search", Admin: true, On: []string{"shop.search"}, Args: json.RawMessage(`{}`)})
	if err != nil || res.Text != "ok" || !res.External {
		t.Fatalf("a tool that is on: %+v, %v", res, err)
	}
}

func specNames(ss []tool.Spec) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

// The first mcp.use of a server in a session names the notes' topic and, when
// the server sent instructions, gives them as outside content; a later one
// adds neither (R-MCP-11).
func TestMcpUseDeliversInstructionsAndNamesTheNotesOnce(t *testing.T) {
	raws := []json.RawMessage{
		mcpclienttest.Tool("search", "Search.", "", true),
		mcpclienttest.Tool("buy", "Buy.", "", false),
	}
	policies := map[string]tool.Policy{"search": tool.PolicyAuto, "buy": tool.PolicyConfirm}
	const topic = `memory topic "mcp shop" with memory.write`
	tests := []struct {
		name         string
		instructions any
		on           []string
		wantInstr    bool
		wantTopic    bool
	}{
		{"first use, instructions", "Filter by month.", nil, true, true},
		{"first use, none", nil, nil, false, true},
		{"first use, not a string", 42, nil, false, true},
		{"later use, instructions", "Filter by month.", []string{"shop.search"}, false, false},
		{"later use, none", nil, []string{"shop.search"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: raws, Instructions: tt.instructions})
			r := newRig(t)
			r.hub.Set([]mcpclient.Server{config("shop", srv, raws, policies)})
			r.await(t, "shop", mcpclient.StateConnected)
			res, err := run(meta(t, r.hub, "mcp.use"), tt.on, `{"server":"shop","tools":["buy"]}`)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(res.Text, "Filter by month."); got != tt.wantInstr {
				t.Errorf("instructions in text = %v, want %v: %q", got, tt.wantInstr, res.Text)
			}
			if res.External != tt.wantInstr {
				t.Errorf("External = %v, want %v", res.External, tt.wantInstr)
			}
			if got := strings.Contains(res.Text, topic); got != tt.wantTopic {
				t.Errorf("topic line = %v, want %v: %q", got, tt.wantTopic, res.Text)
			}
			if !strings.HasPrefix(res.Text, "on from the next step: shop.buy") {
				t.Errorf("text does not start with what was turned on: %q", res.Text)
			}
		})
	}
}
