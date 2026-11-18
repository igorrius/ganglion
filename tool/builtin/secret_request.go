package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/tool"
)

// SecretRequestTool is the name of the tool that asks the owner for a
// secret from the chat (R-INT-2).
const SecretRequestTool = "secret.request"

// SecretAsker opens a secret request and sends its card (intake.Asker).
type SecretAsker interface {
	Ask(ctx context.Context, k intake.SecretAsk, done intake.SecretDoneFunc) (intake.Asked, error)
}

// ManifestFunc parses the manifest of a module's source in the workspace,
// by its short name (Modules.SourceManifest).
type ManifestFunc func(module string) (runner.Manifest, error)

// errNoModuleSources is the ManifestFunc of a node without the module
// tools (no git): there is no module to ask for.
var errNoModuleSources = errors.New("this node has no module sources: it has no git, so no module tools")

// NoModuleSources is the ManifestFunc of a node without the module tools.
func NoModuleSources(string) (runner.Manifest, error) { return runner.Manifest{}, errNoModuleSources }

// SecretRequest is secret.request: the agent names a secret and says why;
// the node sends the owner a card with a link to the intake page, and the
// value goes from the owner's browser into the store without passing the
// model, the inbox, the history, the journal or the logs (R-INT-2,
// R-INT-7). The agent gets a node note when the request closes (R-INT-6).
// No tool reads a value back.
type SecretRequest struct {
	ask      SecretAsker
	manifest ManifestFunc
	// node are the node's own secrets (the bot token, the providers' keys):
	// no module is granted one, so none is asked for in a module's scope.
	node []string
}

// NewSecretRequest returns secret.request over ask, reading the modules'
// manifests through manifest. node are the node's own secrets' names.
func NewSecretRequest(ask SecretAsker, manifest ManifestFunc, node []string) *SecretRequest {
	return &SecretRequest{ask: ask, manifest: manifest, node: node}
}

// Spec implements tool.Handler. The card is the node's own text and the
// agent never sees a value, so the call needs no confirmation of its own:
// the owner's typing the value is the decision (ADR-0098).
func (*SecretRequest) Spec() tool.Spec {
	return tool.Spec{Name: SecretRequestTool,
		Description: "Ask the owner to enter a secret the node stores, without you ever seeing it. With module (its short name), " +
			"the module's secret: the name must be in its manifest's secret.get, and the module reads it at its next use, with " +
			"no restart. Without module, the node's own secret, named in the node's file as store:NAME. The node sends the owner a " +
			"card with a link; the owner types the value on that page and pastes back a sealed line, which goes straight into the " +
			"node's encrypted store. A [secret] note tells you when it is set, failed or expired, never the value. No tool reads " +
			"a secret, and you never ask the owner to type a value into the chat. why is one line for the owner, shown as your words.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the secret's name, such as WIFI_PASSWORD: a letter or _, then letters, digits, _, . or -"},` +
			`"why":{"type":"string","description":"why it is needed, one line"},` +
			`"module":{"type":"string","description":"the module's short name; leave out for the node's own secret"}},` +
			`"required":["name","why"],"additionalProperties":false}`),
		Class: tool.ClassSideEffect, Policy: tool.PolicyAuto, AdminOnly: true}
}

// secretRequestArgs are secret.request's arguments.
type secretRequestArgs struct {
	Name   string `json:"name"`
	Why    string `json:"why"`
	Module string `json:"module"`
}

// Run implements tool.Handler.
func (t *SecretRequest) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	k, err := t.askOf(c)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", SecretRequestTool, err)
	}
	asked, err := t.ask.Ask(ctx, k, intake.NoSecretDone)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", SecretRequestTool, askError(err))
	}
	return tool.Result{Text: fmt.Sprintf("The owner got a card to enter %s (scope %s). The value goes from their browser straight "+
		"into the node's store; you never see it, and no tool reads it. A [secret] note will say how it ended; the request "+
		"expires at %s. Do not ask the owner to type the value into the chat.", k.Name, k.Scope, asked.Expires.UTC().Format("15:04 UTC"))}, nil
}

// askOf reads and checks the call: the secret's scope, who asks and the
// hosts the value may reach.
func (t *SecretRequest) askOf(c tool.Call) (intake.SecretAsk, error) {
	if c.Grant != nil {
		return intake.SecretAsk{}, errors.New("a scheduled run does not ask for secrets")
	}
	var a secretRequestArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return intake.SecretAsk{}, fmt.Errorf("arguments: %w", err)
	}
	why := strings.Join(strings.Fields(a.Why), " ")
	switch {
	case !secrets.ValidName(a.Name):
		return intake.SecretAsk{}, errors.New("name: a letter or _, then letters, digits, _, . and -, at most 128")
	case why == "" || utf8.RuneCountInString(why) > maxReasonRunes:
		return intake.SecretAsk{}, fmt.Errorf("why: one line of 1 to %d characters", maxReasonRunes)
	}
	k := intake.SecretAsk{Scope: secrets.ScopeNode, Name: a.Name, Asking: intake.AskingNode, Why: why, Admin: c.Principal,
		Transport: c.Transport, Conv: c.Conv, Key: c.Key(), Tainted: c.Tainted}
	if a.Module == "" {
		return k, nil
	}
	hosts, err := t.moduleHosts(a.Module, a.Name)
	if err != nil {
		return intake.SecretAsk{}, err
	}
	k.Scope, k.Asking, k.Who, k.Hosts = secrets.ModuleScope(modstore.LocalPrefix+a.Module), intake.AskingModule, a.Module, hosts
	return k, nil
}

// moduleHosts checks that module's manifest grants it the secret name, and
// that name is not one of the node's own; it returns the hosts the
// manifest lets the module reach, which the card names.
func (t *SecretRequest) moduleHosts(module, name string) ([]string, error) {
	if err := moduleName(SecretRequestTool, module); err != nil {
		return nil, err
	}
	if modstore.NodeSecret(t.node, name) {
		return nil, fmt.Errorf("%s is one of the node's own secrets; no module is granted it", name)
	}
	m, err := t.manifest(module)
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", module, err)
	}
	if !slices.Contains(m.Capabilities.SecretGet, name) {
		return nil, fmt.Errorf("the manifest of %s does not list %s under secret.get; add it there first", module, name)
	}
	hosts := slices.Clone(m.Capabilities.HTTPFetch)
	slices.Sort(hosts)
	return slices.Compact(hosts), nil
}

// askError says what a refused request means, for the model to tell the
// owner.
func askError(err error) error {
	switch {
	case errors.Is(err, intake.ErrPageChanged), errors.Is(err, intake.ErrPageUnchecked):
		return errors.New("the intake page failed its check, so handing secrets over from the chat is stopped; " +
			"the owner was told, and can set the secret on the host with `ganglion secret set`")
	case errors.Is(err, intake.ErrTooMany):
		return errors.New("too many requests are open; wait for one to be answered or to expire")
	}
	return err
}
