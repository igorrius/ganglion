package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/tool"
)

// Names of the two tools the agent always has while a server is set.
const (
	ToolsTool = "mcp.tools"
	UseTool   = "mcp.use"
)

// Limits of what a session has turned on (R-MCP-4a).
const (
	// MaxOn is the most on-demand tools on in a session.
	MaxOn = 16
	// MaxOnBytes is the most bytes of definitions (name, description,
	// schema) of the tools on in a session.
	MaxOnBytes = 48 << 10
	// maxUseNames is the most names one mcp.use call takes.
	maxUseNames = 16
	// maxFirstLine is how much of a description mcp.tools shows, in runes.
	maxFirstLine = 200
)

// errNotOffered is a call of mcp.tools or mcp.use while no server is set.
var errNotOffered = errors.New("no remote MCP server is configured")

// Tools are the tools that find the servers' tools and turn them on:
// mcp.tools and mcp.use. Both read only the node's approved copy and send
// nothing off the node, so both are LocalSafe; they are on offer while at
// least one server is set (R-MCP-4a).
func (h *Hub) Tools() []tool.Handler {
	return []tool.Handler{listTool{hub: h}, useTool{hub: h}}
}

var (
	listSchema = json.RawMessage(`{"type":"object","properties":{"server":{"type":"string","description":"The server whose tools to list; none lists the servers."}},"additionalProperties":false}`)
	useSchema  = json.RawMessage(`{"type":"object","properties":{"server":{"type":"string","description":"The server the tools belong to."},"tools":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":16,"description":"Names of the tools to turn on, as mcp.tools lists them."}},"required":["server","tools"],"additionalProperties":false}`)
)

// listTool is mcp.tools.
type listTool struct{ hub *Hub }

// Spec implements tool.Handler.
func (listTool) Spec() tool.Spec {
	return tool.Spec{Name: ToolsTool, InputSchema: listSchema, Class: tool.ClassRead, Policy: tool.PolicyAuto,
		AdminOnly: true, LocalSafe: true,
		Description: "List the remote MCP servers and their state, or with a server the tools it offers: name, policy and the first line of each description. A tool is used after mcp.use turns it on."}
}

// Offered implements tool.Gated: only while a server is set.
func (t listTool) Offered() bool { return t.hub.configured() }

// Run implements tool.Handler. The text comes from the approved copy, the
// same standing as a tool's definition (R-MCP-4a).
func (t listTool) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	if !t.Offered() {
		return tool.Result{}, errNotOffered
	}
	if err := tool.CheckArgs(t.Spec(), c.Args); err != nil {
		return tool.Result{}, err
	}
	var a struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Result{}, tool.ErrBadArgs
	}
	if a.Server == "" {
		return tool.Result{Text: t.hub.serversText()}, nil
	}
	s, err := t.hub.known(a.Server)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: s.toolsText()}, nil
}

// known is the server called name, or an error that names the ones there are.
func (h *Hub) known(name string) (*serverConn, error) {
	if s, ok := h.conn(name); ok {
		return s, nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return nil, fmt.Errorf("%s: no server called %q; the servers are: %s", ToolsTool, text.Truncate(name, 64), strings.Join(h.names(), ", "))
}

// serversText lists the servers with their states.
func (h *Hub) serversText() string {
	var b strings.Builder
	for _, st := range h.States() {
		fmt.Fprintf(&b, "%s: %s, %d tools", st.Name, st.State, st.Tools)
		if len(st.Skipped) > 0 {
			fmt.Fprintf(&b, ", %d not taken", len(st.Skipped))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// toolsText lists the server's approved tools; a server that is not
// connected lists none and says why.
func (s *serverConn) toolsText() string {
	st, tools := s.snapshot()
	if st != StateConnected {
		return fmt.Sprintf("%s is %s: no tools can be turned on now", s.cfg.Name, st)
	}
	var b strings.Builder
	for _, t := range tools {
		policy := s.cfg.Policies[t.Short]
		if policy == tool.PolicyForbidden {
			fmt.Fprintf(&b, "%s [forbidden]: off by the admin's policy; it cannot be turned on\n", t.Name)
			continue
		}
		fmt.Fprintf(&b, "%s [%s]: %s\n", t.Name, policy, firstLine(describe(s.cfg.Name, t), maxFirstLine))
	}
	if b.Len() == 0 {
		return s.cfg.Name + " has no approved tools"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// useTool is mcp.use.
type useTool struct{ hub *Hub }

// Spec implements tool.Handler.
func (useTool) Spec() tool.Spec {
	return tool.Spec{Name: UseTool, InputSchema: useSchema, Class: tool.ClassRead, Policy: tool.PolicyAuto,
		AdminOnly: true, LocalSafe: true,
		Description: "Turn tools of a remote MCP server on for this session, by the names mcp.tools lists. They can be called from the next step and stay until the session ends; at most 16 tools at once."}
}

// Offered implements tool.Gated: only while a server is set.
func (t useTool) Offered() bool { return t.hub.configured() }

// useArgs are the arguments of mcp.use.
type useArgs struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
}

// Run implements tool.Handler: it turns the tools on or refuses, and then
// nothing is turned on. The agent adds Result.On to the session.
func (t useTool) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	if !t.Offered() {
		return tool.Result{}, errNotOffered
	}
	if err := tool.CheckArgs(t.Spec(), c.Args); err != nil {
		return tool.Result{}, err
	}
	var a useArgs
	if err := json.Unmarshal(c.Args, &a); err != nil || a.Server == "" || len(a.Tools) == 0 || len(a.Tools) > maxUseNames {
		return tool.Result{}, tool.ErrBadArgs
	}
	s, err := t.hub.known(a.Server)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", UseTool, err)
	}
	picked, err := s.pick(a.Tools)
	if err != nil {
		return tool.Result{}, err
	}
	if err := t.hub.withinLimits(c.TurnedOn, picked); err != nil {
		return tool.Result{}, err
	}
	res := tool.Result{Text: onText(c.TurnedOn, picked), On: names(picked)}
	if !s.anyOn(c.TurnedOn) {
		res.Text += s.firstUseText()
		res.External = s.client.Instructions() != ""
	}
	return res, nil
}

// anyOn reports whether a tool of the server is among the registry names on.
func (s *serverConn) anyOn(on []string) bool {
	_, tools := s.snapshot()
	return slices.ContainsFunc(tools, func(t Tool) bool { return slices.Contains(on, t.Name) })
}

// NotesTopic is the title of the memory topic that holds what the model
// learns about a server's data (R-MCP-11).
func NotesTopic(server string) string { return "mcp " + server }

// firstUseText is what the first mcp.use of a server in a session adds
// (R-MCP-11): the server's own instructions, labelled as its text, and the
// topic where notes about its data go. The instructions make the result
// outside content, which the agent wraps as data.
func (s *serverConn) firstUseText() string {
	var b strings.Builder
	if in := s.client.Instructions(); in != "" {
		fmt.Fprintf(&b, "\n\nInstructions from the server %s itself (its own text, not the node's):\n%s", s.cfg.Name, in)
	}
	fmt.Fprintf(&b, "\n\nKeep notes about %[1]s in the memory topic %[2]q (open it with a one-line summary when it is not in the known facts). Before you answer, in this turn, call memory.write once with what the next turn would otherwise have to find out again: a call of %[1]s that was refused for its arguments and what it needed, a field whose meaning you had to work out or guess, a filter that must be set. Skip what the topic already says. Never a secret or a person's data.",
		s.cfg.Name, NotesTopic(s.cfg.Name))
	return b.String()
}

// pick resolves the names the model gave, short, full or the server's own,
// against the approved copy. A server that is not connected, a name that is
// not approved and a `forbidden` tool are refusals.
func (s *serverConn) pick(given []string) ([]Tool, error) {
	st, tools := s.snapshot()
	if st != StateConnected {
		return nil, fmt.Errorf("%s: %s is %s; no tool can be turned on now", UseTool, s.cfg.Name, st)
	}
	var out []Tool
	for _, g := range given {
		i := slices.IndexFunc(tools, func(t Tool) bool { return g == t.Name || g == t.Short || g == t.Own })
		if i < 0 {
			return nil, fmt.Errorf("%s: %s has no approved tool %q; mcp.tools lists them", UseTool, s.cfg.Name, text.Truncate(g, 64))
		}
		if s.cfg.Policies[tools[i].Short] == tool.PolicyForbidden {
			return nil, fmt.Errorf("%s: %s is off by the admin's policy and cannot be turned on", UseTool, tools[i].Name)
		}
		if !slices.ContainsFunc(out, func(t Tool) bool { return t.Name == tools[i].Name }) {
			out = append(out, tools[i])
		}
	}
	return out, nil
}

// withinLimits refuses turning picked on when the tools already on plus
// them are over MaxOn tools or MaxOnBytes bytes of definitions. A tool
// already on is counted once, through the hub's current definition.
func (h *Hub) withinLimits(on []string, picked []Tool) error {
	all := slices.Clone(on)
	for _, t := range picked {
		if !slices.Contains(all, t.Name) {
			all = append(all, t.Name)
		}
	}
	if len(all) > MaxOn {
		return fmt.Errorf("%s: that would turn on %d tools; at most %d can be on in a session (%d are on now)", UseTool, len(all), MaxOn, len(on))
	}
	if n := h.definitionBytes(all); n > MaxOnBytes {
		return fmt.Errorf("%s: that would put %d bytes of tool definitions in the session; at most %d (%d tools are on now)", UseTool, n, MaxOnBytes, len(on))
	}
	return nil
}

// definitionBytes is the size of the definitions of the named tools in the
// approved copies the hub holds, whether the server is connected now or
// not: a tool on from a server that is unreachable or whose list changed
// still counts. A tool no server has any longer is not counted.
func (h *Hub) definitionBytes(names []string) int {
	h.mu.RLock()
	conns := slices.Collect(maps.Values(h.servers))
	h.mu.RUnlock()
	n := 0
	for _, s := range conns {
		_, tools := s.snapshot()
		for _, t := range tools {
			if slices.Contains(names, t.Name) {
				n += len(t.Name) + len(describe(s.cfg.Name, t)) + len(t.InputSchema)
			}
		}
	}
	return n
}

// names are the registry names of tools.
func names(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

// onText says what mcp.use turned on and what already was.
func onText(already []string, picked []Tool) string {
	var fresh, had []string
	for _, t := range picked {
		if slices.Contains(already, t.Name) {
			had = append(had, t.Name)
		} else {
			fresh = append(fresh, t.Name)
		}
	}
	var parts []string
	if len(fresh) > 0 {
		parts = append(parts, "on from the next step: "+strings.Join(fresh, ", "))
	}
	if len(had) > 0 {
		parts = append(parts, "already on: "+strings.Join(had, ", "))
	}
	return strings.Join(parts, "; ")
}
