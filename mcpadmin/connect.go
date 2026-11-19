package mcpadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/urlnorm"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// Names of the admin's tools.
const (
	ToolConnect = "mcp.connect"
	ToolRule    = "mcp.rule"
)

// Placeholders of the texts.
const (
	varServer = "server"
	varHost   = "host"
	varURL    = "url"
	varN      = "n"
	varTool   = "tool"
	varFrom   = "from"
	varTo     = "to"
	varTime   = "time"
	varState  = "state"
	varKind   = "kind"
	varLink   = "link"
	varSignIn = "signin_host"
)

// coreAreas are names a server never takes, whatever the registry offers
// at the moment: the tools that manage servers and secrets.
var coreAreas = []string{"mcp", "secret", "config", "tool", "node"}

// connectSchema is mcp.connect's arguments.
var connectSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"name":{"type":"string","description":"A short name for the server, 1 to 24 of a-z, 0-9 and _, such as shop. Its tools are named <name>.<tool>."},` +
	`"url":{"type":"string","description":"The server's Streamable HTTP address, https, as its documentation gives it."},` +
	`"header":{"type":"string","description":"Only for a server that takes a key in a header and offers no sign-in: the header's name from its documentation, such as X-API-Key. Never the key itself."},` +
	`"rules":{"type":"object","description":"The policy of each tool the owner named, by the tool's name as the server lists it: auto runs by itself, confirm asks the owner first, forbidden is never offered.","additionalProperties":{"type":"string","enum":["auto","confirm","forbidden"]}},` +
	`"others":{"type":"string","enum":["auto","confirm","forbidden"],"description":"The policy of every tool not in rules, such as forbidden when the owner needs only the tools named."}},` +
	`"required":["name","url"],"additionalProperties":false}`)

// connectArgs are mcp.connect's arguments.
type connectArgs struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Header string `json:"header"`
	// Rules and Others are the scope the owner stated, as the model took it
	// from their words (R-MCP-3); they shape the card's proposal only.
	Rules  map[string]settings.MCPPolicy `json:"rules"`
	Others settings.MCPPolicy            `json:"others"`
}

// target is a checked connect: the server's name, address and header, and
// the proposal the owner's words asked for.
type target struct {
	name, url, header string
	rules             map[string]settings.MCPPolicy
	others            settings.MCPPolicy // "" when the call gave none
}

// host is the target's host, for the cards.
func (t target) host() string {
	u, err := url.Parse(t.url)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// proposalWords tells the model, after a card, that it holds the scope it
// passed.
func (t target) proposalWords() string {
	if len(t.rules) == 0 && t.others == "" {
		return ""
	}
	return " with the rules you passed"
}

// who is the admin a connect answers, in their private chat.
type who struct {
	principal, transport, conv string
}

// Errors of mcp.connect the model reads: the node's own words, never an
// address, a header's value or a server's text.
var (
	errBadName = errors.New("the name must be 1 to 24 of a-z, 0-9 and _")
	errTaken   = errors.New("that name is taken by the node's own tools or a module: choose another")
	errBadURL  = errors.New("the address must be https with a host and no user name or password")
	errHeader  = errors.New("header must be the name of a header, such as X-API-Key, and not one the client sets itself")
	errGrant   = errors.New("a scheduled run does not connect servers")
	errPrivate = errors.New("servers are connected only in the admin's private chat")
	errMoved   = errors.New("a server by that name is connected at another address: the owner disconnects it with /mcp first, " +
		"or choose another name")
)

// connectTool is mcp.connect (R-MCP-3).
type connectTool struct{ a *Admin }

// Spec implements tool.Handler. It asks first only in a tainted session,
// where the address could carry the session's data out (the policy's own
// rule); the server's tools join only after the admin's Connect.
func (connectTool) Spec() tool.Spec {
	return tool.Spec{Name: ToolConnect, InputSchema: connectSchema, Class: tool.ClassSideEffect, Policy: tool.PolicyAuto, AdminOnly: true,
		Description: "Connect a remote MCP server (Streamable HTTP) the owner asked for. The node checks the address and asks the server. " +
			"When it wants a sign-in, the owner gets a card with a sign-in link; when it needs none, the owner gets a card with its tools at once. " +
			"Nothing is connected until the owner presses Connect on the tools card; you get a note then. " +
			"Call it again with the same name to approve a server's changed tools. Never put a key or a token in any argument."}
}

// Question implements tool.Questioner: the question in a tainted session.
func (t connectTool) Question(_ context.Context, c tool.Call) (string, error) {
	tg, err := t.a.target(c)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolConnect, err)
	}
	return c.Speak.Text(texts.AskMCPConnect, texts.Vars{varServer: text.CodeSpan(tg.name), varURL: texts.Escape(tg.url)}), nil
}

// Run implements tool.Handler.
func (t connectTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	tg, err := t.a.target(c)
	if err != nil {
		t.a.in.connect(ctx, outcomeRefused)
		return tool.Result{}, fmt.Errorf("%s: %w", ToolConnect, err)
	}
	if c.Grant != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolConnect, errGrant)
	}
	if c.Transport == "" || c.Conv == "" || c.Principal == "" {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolConnect, errPrivate)
	}
	res, err := t.a.connect(ctx, c, tg)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolConnect, err)
	}
	return res, nil
}

// target reads and checks the call's arguments (R-MCP-2, R-MCP-3): the
// address as web.fetch checks it before the network; whether its host is
// public is the dialer's check, on every request.
func (a *Admin) target(c tool.Call) (target, error) {
	var args connectArgs
	dec := json.NewDecoder(bytes.NewReader(c.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return target{}, errors.New("arguments must be name, url and optionally header, rules and others")
	}
	rules, err := checkRules(args.Rules, args.Others)
	if err != nil {
		return target{}, err
	}
	if !settings.MCPNameRE.MatchString(args.Name) {
		return target{}, errBadName
	}
	if slices.Contains(coreAreas, args.Name) || slices.Contains(a.d.Reserved(), args.Name) {
		return target{}, errTaken
	}
	norm, ok := urlnorm.Normalize(args.URL)
	if !ok || !settings.ValidMCPURL(norm) {
		return target{}, errBadURL
	}
	if args.Header != "" && !settings.ValidMCPHeader(args.Header) {
		return target{}, errHeader
	}
	return target{name: args.Name, url: norm, header: args.Header, rules: rules, others: args.Others}, nil
}

// errBadRules refuses rules or others that are not policies of named tools.
var errBadRules = errors.New("rules must map a tool's name to auto, confirm or forbidden, and others must be one of those")

// checkRules checks the policies the call carries and keys them by the
// tool's short name, as the server's list gives it.
func checkRules(rules map[string]settings.MCPPolicy, others settings.MCPPolicy) (map[string]settings.MCPPolicy, error) {
	if others != "" && !validPolicy(others) {
		return nil, errBadRules
	}
	out := make(map[string]settings.MCPPolicy, len(rules))
	for name, p := range rules {
		if name == "" || !validPolicy(p) {
			return nil, errBadRules
		}
		out[name] = p
	}
	return out, nil
}

// validPolicy reports whether p is one of the three policies.
func validPolicy(p settings.MCPPolicy) bool {
	switch p {
	case settings.MCPPolicyAuto, settings.MCPPolicyConfirm, settings.MCPPolicyForbidden:
		return true
	}
	return false
}

// connect probes the server without a credential and goes on by its
// answer: the tools card when it needs no sign-in, the sign-in card when
// it answers 401, the key's request when a header was named.
func (a *Admin) connect(ctx context.Context, c tool.Call, t target) (tool.Result, error) {
	w := who{principal: c.Principal, transport: c.Transport, conv: c.Conv}
	// A name keeps one sign-in: a sign-in at another address would hand
	// the running server another server's token.
	if line, ok, err := a.line(t.name); err != nil {
		return tool.Result{}, err
	} else if ok && line.URL != t.url {
		a.in.connect(ctx, outcomeRefused)
		return tool.Result{}, errMoved
	}
	if t.header != "" {
		return a.requestKey(ctx, c, w, t)
	}
	probe := a.client(t, mcpclient.NoAuth{})
	err := probe.Initialize(ctx)
	switch {
	case err == nil:
		out, n, err := a.toolsCard(ctx, c.Speak, w, t, settings.MCPAuthNone, probe, c.Key()+":mcp.card")
		if err != nil {
			a.in.connect(ctx, outcomeFailed)
			return tool.Result{}, err
		}
		a.in.connect(ctx, outcomeCard)
		return tool.Result{Text: fmt.Sprintf("The server %s needs no sign-in. The owner got a card with its %d tools to approve%s; "+
			"nothing is connected until they press Connect, and you get a note then.", t.name, n, t.proposalWords()), Out: []event.Outgoing{out}}, nil
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return a.signIn(ctx, c, w, t)
	}
	a.in.connect(ctx, outcomeFailed)
	return tool.Result{}, fmt.Errorf("the server %s %s", t.name, failureWords(err))
}

// requestKey asks the admin for the key of a server with auth: header
// through a secret request (R-MCP-3, R-INT-2): the card names the server,
// the header and the one host the key goes to; once the key is stored the
// node lists the tools with it and sends the tools card. The key never
// passes through the model.
func (a *Admin) requestKey(ctx context.Context, c tool.Call, w who, t target) (tool.Result, error) {
	err := a.d.Keys.Request(ctx, KeyRequest{Server: t.name, Header: t.header, Host: t.host(),
		Principal: w.principal, Transport: w.transport, Conv: w.conv, Key: c.Key(), Tainted: c.Tainted,
		Done: func(ctx context.Context) {
			if err := a.listAfterSignIn(ctx, w, t, settings.MCPAuthHeader, c.Key()+":mcp.card"); err != nil {
				a.log.Warn("mcpadmin: the tools card was not sent", "server", t.name, "kind", "outbox")
			}
		}})
	if err != nil {
		a.in.connect(ctx, outcomeFailed)
		return tool.Result{}, fmt.Errorf("the key of %s was not asked for: %s", t.name, keyWords(err))
	}
	a.in.connect(ctx, outcomeSignIn)
	return tool.Result{Text: fmt.Sprintf("The owner got a card to enter the key of %s for the header %s; it never passes through you. "+
		"Once it is stored, the node lists the tools and sends a card to approve them; you get a note when the key is in.",
		t.name, t.header)}, nil
}

// signIn begins the OAuth sign-in and makes its card (R-MCP-3).
func (a *Admin) signIn(ctx context.Context, c tool.Call, w who, t target) (tool.Result, error) {
	req, err := a.d.Requests.OpenOAuth(ctx, a.answered(w, t))
	if err != nil {
		a.in.connect(ctx, outcomeFailed)
		return tool.Result{}, fmt.Errorf("the node cannot open a sign-in now (%s); try again later", requestWords(err))
	}
	si, err := a.d.SignIns.Begin(ctx, t.name, t.url, req.ID)
	if errors.Is(err, mcpclient.ErrNoSignIn) {
		a.in.connect(ctx, outcomeRefused)
		return tool.Result{}, fmt.Errorf("the server %s refuses a request without a credential and offers no OAuth sign-in: "+
			"it probably takes a key in a header. Read its documentation for the header's name and call %s again with header", t.name, ToolConnect)
	}
	if err != nil {
		a.in.connect(ctx, outcomeFailed)
		return tool.Result{}, fmt.Errorf("the sign-in of %s %s", t.name, signInWords(err))
	}
	text := c.Speak.Text(texts.AskMCPSignIn, texts.Vars{varServer: text.CodeSpan(t.name), varHost: texts.Escape(si.ServerHost),
		varSignIn: texts.Escape(si.SignInHost),
		varLink:   "[" + texts.Escape(c.Speak.Text(texts.AskMCPSignInLink, nil)) + "](<" + si.URL + ">)"})
	if req.PageErr != nil {
		text += "\n\n" + c.Speak.Text(texts.AskMCPPageWarning, nil)
	}
	a.in.connect(ctx, outcomeSignIn)
	out := event.Outgoing{IdemKey: c.Key() + ":mcp.sign_in", Transport: c.Transport, Conv: c.Conv, Text: text, NoPreview: true}
	return tool.Result{Text: fmt.Sprintf("The server %s wants a sign-in. The owner got a card with a sign-in link at %s; "+
		"after they sign in and paste the line the page shows, the node lists the tools and sends a card to approve them. "+
		"Wait for the owner; nothing is connected until they press Connect, and you get a note then.", t.name, si.SignInHost),
		Out: []event.Outgoing{out}}, nil
}

// answered is what the sign-in's intake request hands its code to: the
// exchange, then the list and the tools card. A code that is not accepted
// fails the request, and intake tells the admin; a list that fails after
// a good sign-in is told here.
func (a *Admin) answered(w who, t target) intake.OAuthFunc {
	return func(ctx context.Context, ans intake.OAuthAnswer) error {
		if err := a.d.SignIns.Complete(ctx, ans); err != nil {
			a.in.connect(ctx, outcomeFailed)
			a.log.Warn("mcpadmin: a sign-in was not completed", "server", t.name, "kind", kindOf(err))
			return err
		}
		return a.listAfterSignIn(ctx, w, t, settings.MCPAuthOAuth, "mcp:card:"+ans.ID)
	}
}

// listAfterSignIn lists the tools with the new credential and sends the
// tools card, or tells the admin that the list failed.
func (a *Admin) listAfterSignIn(ctx context.Context, w who, t target, auth settings.MCPAuth, key string) error {
	sp := a.speaker(ctx, w.principal)
	c := a.client(t, a.auth(t.name, settings.MCPServer{Auth: auth, Header: t.header}))
	err := c.Initialize(ctx)
	var out event.Outgoing
	if err == nil {
		out, _, err = a.toolsCard(ctx, sp, w, t, auth, c, key)
	}
	if err != nil {
		a.in.connect(ctx, outcomeFailed)
		a.log.Warn("mcpadmin: the tools of a server were not listed after its sign-in", "server", t.name, "kind", kindOf(err))
		out = event.Outgoing{IdemKey: key + ":failed", Transport: w.transport, Conv: w.conv,
			Text: sp.Text(texts.MCPListFailed, texts.Vars{varServer: text.CodeSpan(t.name), varKind: texts.Escape(kindOf(err))})}
	} else {
		a.in.connect(ctx, outcomeCard)
	}
	if err := a.d.Enqueue(ctx, out); err != nil {
		return fmt.Errorf("mcpadmin: send the card of %s: %w", t.name, err)
	}
	return nil
}

// client is a probe's client of the target with auth.
func (a *Admin) client(t target, auth mcpclient.Auth) *mcpclient.Client {
	return mcpclient.NewClient(t.name, t.url, auth, settings.DefaultMCPTimeout,
		mcpclient.WithHTTPClient(a.d.HTTP), mcpclient.WithVersion(a.version))
}

// kindOf is an error's kind for a log line, a metric or the admin: a
// fixed word, never its text.
func kindOf(err error) string {
	for _, k := range []struct {
		err  error
		kind string
	}{
		{mcpclient.ErrNeedsSignIn, "needs_sign_in"}, {mcpclient.ErrUnauthorized, "unauthorized"},
		{mcpclient.ErrForbidden, "forbidden"}, {mcpclient.ErrUnreachable, kindUnreachable},
		{mcpclient.ErrTooLarge, "too_large"}, {mcpclient.ErrProtocol, "protocol"}, {mcpclient.ErrRedirect, "redirect"},
		{mcpclient.ErrIssuer, "issuer"}, {mcpclient.ErrExchange, "exchange"}, {mcpclient.ErrUnknownSignIn, "unknown_sign_in"},
		{mcpclient.ErrMetadata, "metadata"}, {mcpclient.ErrRegistration, "registration"},
	} {
		if errors.Is(err, k.err) {
			return k.kind
		}
	}
	if errors.As(err, new(*unknownToolsError)) {
		return "unknown_tool"
	}
	if errors.Is(err, errCardTooLong) {
		return "too_many_tools"
	}
	if se, ok := errors.AsType[*mcpclient.StatusError](err); ok {
		return "status_" + strconv.Itoa(se.Code)
	}
	return "other"
}

// failureWords says, for the model, why a probe failed.
func failureWords(err error) string {
	switch kind := kindOf(err); kind {
	case kindUnreachable:
		return "did not answer in time or could not be reached; check the address"
	case "forbidden":
		return "refused the node (403)"
	case "redirect":
		return "redirected to another place; use the address it names in its documentation"
	default:
		return "did not answer as an MCP server (" + kind + "); check the address"
	}
}

// signInWords says, for the model, why a sign-in could not begin.
func signInWords(err error) string {
	switch kindOf(err) {
	case "metadata":
		return "cannot begin: its sign-in metadata is not usable (an address that is not https, or on another host than the server)"
	case "registration":
		return "cannot begin: the sign-in server refused to register the node"
	case kindUnreachable:
		return "cannot begin: the sign-in server did not answer"
	}
	return "cannot begin (" + kindOf(err) + ")"
}

// requestWords says why an intake request was not opened.
func requestWords(err error) string {
	if errors.Is(err, intake.ErrTooMany) {
		return "too many requests are open"
	}
	return "the requests are not available"
}

// kindUnreachable is the kind of a server or a sign-in server that did not
// answer.
const kindUnreachable = "unreachable"

// timeIn formats t for a person in loc.
func timeIn(t time.Time, loc *time.Location) string { return t.In(loc).Format("2 Jan 15:04") }
