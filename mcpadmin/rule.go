package mcpadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// ruleSchema is mcp.rule's arguments.
var ruleSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"server":{"type":"string","description":"The server's name, as mcp.tools lists it."},` +
	`"tool":{"type":"string","description":"The tool, as mcp.tools lists it: <server>.<tool> or the tool's name alone."},` +
	`"policy":{"type":"string","enum":["auto","confirm","forbidden"],"description":"auto runs by itself, confirm asks the owner first, forbidden is never offered."}},` +
	`"required":["server","tool","policy"],"additionalProperties":false}`)

// ruleArgs are mcp.rule's arguments.
type ruleArgs struct {
	Server string             `json:"server"`
	Tool   string             `json:"tool"`
	Policy settings.MCPPolicy `json:"policy"`
}

// rule is a checked mcp.rule call: the change and the policy it replaces.
type rule struct {
	server, tool string
	from, to     settings.MCPPolicy
}

// errRuleArgs and the like refuse an mcp.rule call, in the node's words.
var (
	errRuleArgs   = errors.New("arguments must be server, tool and a policy of auto, confirm or forbidden")
	errNoServer   = errors.New("no MCP server by that name is connected; mcp.tools lists them")
	errNoTool     = errors.New("the server has no approved tool by that name; mcp.tools lists them")
	errSamePolicy = errors.New("the tool has that policy already")
)

// ruleTool is mcp.rule (R-MCP-9): one tool's policy, after a question of
// its own, which the admin answers; it never runs unasked.
type ruleTool struct{ a *Admin }

// Spec implements tool.Handler.
func (ruleTool) Spec() tool.Spec {
	return tool.Spec{Name: ToolRule, InputSchema: ruleSchema, Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true,
		Description: "Change the policy of one tool of a connected MCP server when the owner asks: auto runs it by itself, " +
			"confirm asks the owner first, forbidden never offers it. The owner confirms."}
}

// Question implements tool.Questioner: the change in the owner's words.
func (t ruleTool) Question(_ context.Context, c tool.Call) (string, error) {
	r, err := t.a.ruleOf(c)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolRule, err)
	}
	return c.Speak.Text(texts.AskMCPRule, texts.Vars{varTool: text.CodeSpan(r.server + "." + r.tool),
		varFrom: policyWords(c.Speak, r.from), varTo: policyWords(c.Speak, r.to)}), nil
}

// Run implements tool.Handler: the policy written, recorded and applied.
func (t ruleTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if c.Grant != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, errGrant)
	}
	r, err := t.a.ruleOf(c)
	if errors.Is(err, errSamePolicy) {
		return tool.Result{Text: "The tool has that policy already; nothing changed."}, nil
	}
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, err)
	}
	if err := t.a.d.File.SetPolicy(r.server, r.tool, r.to); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, err)
	}
	if err := t.a.Apply(); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolRule, err)
	}
	t.a.in.change(ctx, changeRule)
	return tool.Result{Text: fmt.Sprintf("The policy of %s.%s is now %s.", r.server, r.tool, r.to)}, nil
}

// ruleOf reads and checks an mcp.rule call against the file.
func (a *Admin) ruleOf(c tool.Call) (rule, error) {
	var args ruleArgs
	dec := json.NewDecoder(bytes.NewReader(c.Args))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil {
		return rule{}, errRuleArgs
	}
	switch args.Policy {
	case settings.MCPPolicyAuto, settings.MCPPolicyConfirm, settings.MCPPolicyForbidden:
	default:
		return rule{}, errRuleArgs
	}
	line, ok, err := a.line(args.Server)
	if err != nil {
		return rule{}, err
	}
	if !ok {
		return rule{}, errNoServer
	}
	name := strings.TrimPrefix(args.Tool, args.Server+".")
	from, ok := line.Tools[name]
	switch {
	case !ok:
		return rule{}, errNoTool
	case from == args.Policy:
		return rule{}, errSamePolicy
	}
	return rule{server: args.Server, tool: name, from: from, to: args.Policy}, nil
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
