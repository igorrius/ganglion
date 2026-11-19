package mcpadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// ruleSchema is mcp.rule's arguments.
var ruleSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"server":{"type":"string","description":"The server's name, as mcp.tools lists it."},` +
	`"changes":{"type":"array","minItems":1,"description":"Every change in this one call, a list of one for one change.","items":{"type":"object","properties":{` +
	`"tool":{"type":"string","description":"The tool, as mcp.tools lists it: <server>.<tool> or the tool's name alone."},` +
	`"policy":{"type":"string","enum":["auto","confirm","forbidden"],"description":"auto runs by itself, confirm asks the owner first, forbidden is never offered."}},` +
	`"required":["tool","policy"],"additionalProperties":false}},` +
	`"others":{"type":"string","enum":["auto","confirm","forbidden"],"description":"The policy of every tool of the server that changes does not name."}},` +
	`"required":["server"],"additionalProperties":false}`)

// ruleArgs are mcp.rule's arguments.
type ruleArgs struct {
	Server  string             `json:"server"`
	Changes []ruleChangeArg    `json:"changes"`
	Others  settings.MCPPolicy `json:"others"`
}

// ruleChangeArg is one change of the call.
type ruleChangeArg struct {
	Tool   string             `json:"tool"`
	Policy settings.MCPPolicy `json:"policy"`
}

// ruleChange is one tool's policy going from one value to another.
type ruleChange struct {
	tool     string
	from, to settings.MCPPolicy
}

// rule is a checked mcp.rule call: the changes that really change a policy.
type rule struct {
	server  string
	changes []ruleChange
}

// errRuleArgs and the like refuse an mcp.rule call, in the node's words.
var (
	errRuleArgs   = errors.New("arguments must be server and changes, a list of {tool, policy} with a policy of auto, confirm or forbidden, and optionally others")
	errNoServer   = errors.New("no MCP server by that name is connected; mcp.tools lists them")
	errSamePolicy = errors.New("every tool has that policy already")
)

// ruleToolError refuses a call that names a tool the server has not approved,
// or names one twice.
type ruleToolError struct {
	kind  string
	names []string
}

func (e *ruleToolError) Error() string {
	shown := e.names
	if len(shown) > maxNamedUnknown {
		shown = shown[:maxNamedUnknown]
	}
	for i, n := range shown {
		shown[i] = text.Truncate(n, 64)
	}
	more := ""
	if len(e.names) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(e.names)-len(shown))
	}
	return e.kind + " " + strings.Join(shown, ", ") + more + "; nothing was changed. mcp.tools lists the approved tools"
}

// maxRuleLines is how many changes the question names one by one; past it,
// the changes that do not turn a tool on are grouped by what they change.
const maxRuleLines = 10

// ruleTool is mcp.rule (R-MCP-9): the policies of several tools, after one
// question of its own, which the admin answers; it never runs unasked.
type ruleTool struct{ a *Admin }

// Spec implements tool.Handler.
func (ruleTool) Spec() tool.Spec {
	return tool.Spec{Name: ToolRule, InputSchema: ruleSchema, Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true,
		Description: "Change the policy of tools of a connected MCP server when the owner asks: auto runs a tool by itself, " +
			"confirm asks the owner first, forbidden never offers it. Put every change the owner asked for in ONE call, in changes " +
			"(a list of one for one change), with others for all the tools it does not name; never call once per tool. " +
			"The owner confirms once for the whole call. A tool the server does not have is refused."}
}

// Question implements tool.Questioner: the changes in the owner's words.
func (t ruleTool) Question(_ context.Context, c tool.Call) (string, error) {
	r, err := t.a.ruleOf(c)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolRule, err)
	}
	return ruleQuestion(c.Speak, r), nil
}

// ruleQuestion is the question of a rule call. A change that turns a tool
// on by itself is always named; the others are named while there are few and
// grouped by what they change after that.
func ruleQuestion(sp texts.Speaker, r rule) string {
	if len(r.changes) == 1 {
		ch := r.changes[0]
		return sp.Text(texts.AskMCPRule, texts.Vars{varTool: text.CodeSpan(r.server + "." + ch.tool),
			varFrom: policyWords(sp, ch.from), varTo: policyWords(sp, ch.to)})
	}
	var b strings.Builder
	b.WriteString(sp.Text(texts.AskMCPRules, texts.Vars{varServer: text.CodeSpan(r.server), varN: fmt.Sprint(len(r.changes))}))
	type pair struct{ from, to settings.MCPPolicy }
	groups := map[pair]int{}
	var order []pair
	for _, ch := range r.changes {
		if ch.to == settings.MCPPolicyAuto || len(r.changes) <= maxRuleLines {
			b.WriteString("\n" + sp.Text(texts.AskMCPRuleLine, texts.Vars{varTool: text.CodeSpan(r.server + "." + ch.tool),
				varFrom: policyWords(sp, ch.from), varTo: policyWords(sp, ch.to)}))
			continue
		}
		k := pair{ch.from, ch.to}
		if groups[k] == 0 {
			order = append(order, k)
		}
		groups[k]++
	}
	for _, k := range order {
		b.WriteString("\n" + sp.Text(texts.AskMCPRuleGroup, texts.Vars{varN: fmt.Sprint(groups[k]),
			varFrom: policyWords(sp, k.from), varTo: policyWords(sp, k.to)}))
	}
	return b.String()
}

// Run implements tool.Handler: the policies written, recorded and applied.
func (t ruleTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if c.Grant != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, errGrant)
	}
	r, err := t.a.ruleOf(c)
	if errors.Is(err, errSamePolicy) {
		return tool.Result{Text: "The tools have those policies already; nothing changed."}, nil
	}
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, err)
	}
	policies := make(map[string]settings.MCPPolicy, len(r.changes))
	for _, ch := range r.changes {
		policies[ch.tool] = ch.to
	}
	if err := t.a.d.File.SetPolicies(r.server, policies); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, err)
	}
	if err := t.a.Apply(); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, err)
	}
	t.a.in.change(ctx, changeRule)
	if len(r.changes) == 1 {
		return tool.Result{Text: fmt.Sprintf("The policy of %s.%s is now %s.", r.server, r.changes[0].tool, r.changes[0].to)}, nil
	}
	return tool.Result{Text: fmt.Sprintf("The policies of %d tools of %s changed.", len(r.changes), r.server)}, nil
}

// ruleOf reads and checks an mcp.rule call as a whole against the file: the
// server, every tool and every policy, before anything is asked or written.
func (a *Admin) ruleOf(c tool.Call) (rule, error) {
	var args ruleArgs
	dec := json.NewDecoder(bytes.NewReader(c.Args))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil || len(args.Changes) == 0 && args.Others == "" ||
		args.Others != "" && !validPolicy(args.Others) {
		return rule{}, errRuleArgs
	}
	line, ok, err := a.line(args.Server)
	if err != nil {
		return rule{}, err
	}
	if !ok {
		return rule{}, errNoServer
	}
	named, err := namedChanges(args, line.Tools)
	if err != nil {
		return rule{}, err
	}
	r := rule{server: args.Server}
	for _, name := range slices.Sorted(maps.Keys(line.Tools)) {
		to, ok := named[name]
		if !ok {
			to = args.Others
		}
		if from := line.Tools[name]; to != "" && to != from {
			r.changes = append(r.changes, ruleChange{tool: name, from: from, to: to})
		}
	}
	if len(r.changes) == 0 {
		return rule{}, errSamePolicy
	}
	return r, nil
}

// namedChanges maps the tools the call names to their new policy, refusing a
// bad policy, a tool the server has no approved line for and a tool named
// twice.
func namedChanges(args ruleArgs, tools map[string]settings.MCPPolicy) (map[string]settings.MCPPolicy, error) {
	named := make(map[string]settings.MCPPolicy, len(args.Changes))
	var unknown, twice []string
	for _, ch := range args.Changes {
		if !validPolicy(ch.Policy) {
			return nil, errRuleArgs
		}
		name := strings.TrimPrefix(ch.Tool, args.Server+".")
		switch _, dup := named[name]; {
		case !hasTool(tools, name):
			unknown = append(unknown, ch.Tool)
		case dup:
			twice = append(twice, ch.Tool)
		default:
			named[name] = ch.Policy
		}
	}
	if len(unknown) > 0 {
		return nil, &ruleToolError{kind: "the server has no approved tool named", names: unknown}
	}
	if len(twice) > 0 {
		return nil, &ruleToolError{kind: "the call names twice", names: twice}
	}
	return named, nil
}

// hasTool reports whether the server's approved line has the tool.
func hasTool(tools map[string]settings.MCPPolicy, name string) bool {
	_, ok := tools[name]
	return ok
}

// policyWords is a policy in the owner's words.
func policyWords(sp texts.Speaker, p settings.MCPPolicy) string {
	switch p {
	case settings.MCPPolicyAuto:
		return sp.Text(texts.AskMCPPolicyAuto, nil)
	case settings.MCPPolicyForbidden:
		return sp.Text(texts.AskMCPPolicyForbidden, nil)
	case settings.MCPPolicyConfirm:
	}
	return sp.Text(texts.AskMCPPolicyConfirm, nil)
}

// Tools are the admin's tools: mcp.connect and mcp.rule.
func (a *Admin) Tools() []tool.Handler {
	return []tool.Handler{connectTool{a: a}, ruleTool{a: a}}
}
