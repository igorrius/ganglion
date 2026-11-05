// Package policy decides, before a tool runs, whether the call may run at
// once, needs the owner's confirmation, or is refused (spec R-POL-*,
// R-AGT-4; design section 4). The decision is a pure function of the
// tool's spec and the call's context; the middleware around it only acts on
// the decision, so the executor enforces and the model cannot talk its way
// past it (spec principle 6).
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/urlnorm"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
)

// Tools whose calls a rule looks at by name.
const (
	toolWebFetch     = "web.fetch"
	toolMessageSend  = "message.send"
	toolMemoryWrite  = "memory.write"
	toolMemoryForget = "memory.forget"
)

const (
	// confirmText is what the model reads for a parked call. It says the
	// call has not run, so the model does not report it as done (ruling
	// R48: the live run of 2026-11-03 said "I updated the record" while
	// the memory write waited for the press).
	confirmText = "not done yet: awaiting confirmation from the owner; tell them it waits for their answer, " +
		"never that it is done. The conversation continues when they answer"
	// questionArgsRunes caps the arguments shown in a confirmation
	// question of a tool that does not word its own; a tool whose
	// arguments must be shown whole is a tool.Questioner (ruling R34).
	questionArgsRunes = 300
)

// AlreadyWaiting is what the model reads for a call identical to one
// that already waits for the owner's answer in the conversation: it is
// not asked again (ruling R38).
const AlreadyWaiting = "this exact call is already waiting for the owner's confirmation; do not ask again: " +
	"the conversation continues when they answer"

// DefaultMaxDepth is the deepest chain of agent-started turns allowed
// (R-AGT-4).
const DefaultMaxDepth = 3

// ErrNoAsker is what NoAsk answers: confirmations are not available.
var ErrNoAsker = errors.New("policy: confirmations are not available")

// Asker parks a call for a person (tool/pending). Ask returns the pending
// id and never an empty one without an error; Outgoing is the message that
// asks, which goes out with the turn's replies. Parked returns the id of a
// call identical to c (tool and canonical arguments) that already waits
// in c's conversation, other than c itself, or "" (ruling R38).
type Asker interface {
	Parked(ctx context.Context, c tool.Call) (pending string, err error)
	Ask(ctx context.Context, c tool.Call, question string) (pending string, err error)
	Outgoing(ctx context.Context, pending string) (event.Outgoing, error)
}

// NoAsk is the Asker of a node without confirmations: every call that
// needs one fails, so it never runs.
type NoAsk struct{}

// Parked implements Asker: nothing is ever parked.
func (NoAsk) Parked(context.Context, tool.Call) (string, error) { return "", nil }

// Ask implements Asker.
func (NoAsk) Ask(context.Context, tool.Call, string) (string, error) { return "", ErrNoAsker }

// Outgoing implements Asker.
func (NoAsk) Outgoing(context.Context, string) (event.Outgoing, error) {
	return event.Outgoing{}, ErrNoAsker
}

// Config holds the policy settings.
type Config struct {
	MaxDepth int // calls deeper than this are forbidden
	// Overrides replace a tool's default policy by tool name. None in M3b;
	// the node YAML of M4 fills it.
	Overrides map[string]tool.Policy
}

// DefaultConfig returns the usual settings.
func DefaultConfig() Config { return Config{MaxDepth: DefaultMaxDepth} }

// Option configures a Policy.
type Option func(*Policy)

// WithAlerts sets who hears about a call outside a task's grant. The
// default alerts nobody.
func WithAlerts(a alert.Admins) Option { return func(p *Policy) { p.alerts = a } }

// WithClock sets the clock and the location whose calendar day limits the
// grant alert to one per task per day. The default is time.Now in UTC.
func WithClock(now func() time.Time, loc *time.Location) Option {
	return func(p *Policy) { p.now, p.loc = now, loc }
}

// WithLogger sets where a failed alert is reported. The default drops it.
func WithLogger(l *slog.Logger) Option { return func(p *Policy) { p.log = l } }

// Policy is the middleware that applies Decide to every call.
type Policy struct {
	cfg    Config
	reg    *tool.Registry
	ask    Asker
	alerts alert.Admins
	now    func() time.Time
	loc    *time.Location
	log    *slog.Logger
}

// New returns a policy over the registry's tools; confirmations go to ask.
func New(cfg Config, reg *tool.Registry, ask Asker, opts ...Option) *Policy {
	p := &Policy{
		cfg: cfg, reg: reg, ask: ask, now: time.Now, loc: time.UTC, log: slog.New(slog.DiscardHandler),
		alerts: alert.Admins{List: func(context.Context) ([]string, error) { return nil, nil }},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Middleware decides each call. In a scheduled task's turn, a tool that
// is not registered is outside the grant. Arguments that are not exactly the
// tool's declared parameters are refused first (tool.CheckArgs, ruling
// R18): the rules below read them by exact name. A confirm parks the call with the Asker
// and does not run it; a forbidden or out-of-grant call returns an error
// for the model and does not run. The decision goes to the journal around
// this middleware (journal.Annotate).
func (p *Policy) Middleware() tool.Middleware {
	return func(next tool.Func) tool.Func {
		return func(ctx context.Context, c tool.Call) (tool.Result, error) {
			h, ok := p.reg.Lookup(c.Name)
			switch {
			case !ok && c.Grant != nil:
				journal.Annotate(ctx, journal.DecisionOutOfGrant)
				return p.outOfGrant(ctx, c)
			case !ok:
				journal.Annotate(ctx, journal.DecisionForbidden)
				return tool.Result{}, fmt.Errorf("policy: %s: %w", c.Name, tool.ErrUnknown)
			}
			if err := tool.CheckArgs(h.Spec(), c.Args); err != nil {
				journal.Annotate(ctx, journal.DecisionForbidden)
				return tool.Result{}, fmt.Errorf("policy: %s: %w", c.Name, err)
			}
			s := p.spec(h.Spec())
			pol, d := Decide(p.cfg.MaxDepth, s, c)
			journal.Annotate(ctx, d)
			switch {
			case d == journal.DecisionOutOfGrant:
				return p.outOfGrant(ctx, c)
			case localRefused(s, c):
				return tool.Result{}, fmt.Errorf("policy: %s may send data off this node and this session is local-only; tell the owner, who can start a new session with /new: %w", c.Name, tool.ErrForbidden)
			case pol == tool.PolicyForbidden:
				return tool.Result{}, fmt.Errorf("policy: %s is not allowed here: %w", c.Name, tool.ErrForbidden)
			case pol == tool.PolicyConfirm:
				return p.confirm(ctx, h, c)
			case pol == tool.PolicyAuto:
				return p.run(ctx, next, c)
			default:
				return tool.Result{}, fmt.Errorf("policy: %s: unknown policy %q: %w", c.Name, pol, tool.ErrForbidden)
			}
		}
	}
}

// run runs an allowed call. A task's call that the tool itself finds
// outside the grant (web.fetch redirected to another host) is an
// out-of-grant call too: journalled so, with the same notice to the
// admins as a call the rules refuse.
func (p *Policy) run(ctx context.Context, next tool.Func, c tool.Call) (tool.Result, error) {
	res, err := next(ctx, c)
	if c.Grant == nil || !errors.Is(err, tool.ErrOutOfGrant) {
		return res, err
	}
	journal.Annotate(ctx, journal.DecisionOutOfGrant)
	alerts, _ := p.outOfGrant(ctx, c)
	res.Out = append(res.Out, alerts.Out...)
	return res, err
}

// spec returns s with its default policy replaced by the configured one.
func (p *Policy) spec(s tool.Spec) tool.Spec {
	if o, ok := p.cfg.Overrides[s.Name]; ok {
		s.Policy = o
	}
	return s
}

// confirm parks the call for the owner and returns its pending id with
// the question, which the agent sends with the turn's replies. A call
// identical to one already waiting is not asked again: it gets that
// call's pending id, so the answer resumes this session too, and the
// fixed AlreadyWaiting text, with no question (ruling R38); the journal
// keeps it as confirm, the decision Decide made.
func (p *Policy) confirm(ctx context.Context, h tool.Handler, c tool.Call) (tool.Result, error) {
	waiting, err := p.ask.Parked(ctx, c)
	if err != nil {
		return tool.Result{}, fmt.Errorf("policy: ask about %s: %w", c.Name, err)
	}
	if waiting != "" {
		return tool.Result{Pending: waiting, Text: AlreadyWaiting}, nil
	}
	q, err := question(ctx, h, c)
	if err != nil {
		return tool.Result{}, err
	}
	id, err := p.ask.Ask(ctx, c, q)
	if err != nil {
		return tool.Result{}, fmt.Errorf("policy: ask about %s: %w", c.Name, err)
	}
	if id == "" {
		return tool.Result{}, fmt.Errorf("policy: ask about %s: the asker returned no pending id", c.Name)
	}
	out, err := p.ask.Outgoing(ctx, id)
	if err != nil {
		return tool.Result{}, fmt.Errorf("policy: question about %s: %w", c.Name, err)
	}
	return tool.Result{Pending: id, Text: confirmText, Out: []event.Outgoing{out}}, nil
}

// question is what the owner is asked, with secrets removed and the
// length capped: the tool's own words when it is a tool.Questioner,
// otherwise the tool and its arguments. A tool that cannot word its
// question refuses the call.
func question(ctx context.Context, h tool.Handler, c tool.Call) (string, error) {
	q, ok := h.(tool.Questioner)
	if !ok {
		return fmt.Sprintf("Run %s with %s?", c.Name, text.Truncate(redact.String(string(c.Args)), questionArgsRunes)), nil
	}
	s, err := q.Question(ctx, c)
	if err != nil {
		return "", fmt.Errorf("policy: %s: %w", c.Name, err)
	}
	return text.Truncate(redact.String(s), tool.MaxQuestionRunes), nil
}

// outOfGrant refuses a call outside a task's grant and alerts the admins,
// once per task per day: the alert's IdemKey carries the day, and the
// outbox stores a message with a known IdemKey once.
func (p *Policy) outOfGrant(ctx context.Context, c tool.Call) (tool.Result, error) {
	err := fmt.Errorf("policy: %s is outside this task's grant: %w", c.Name, tool.ErrOutOfGrant)
	job := strings.TrimPrefix(c.Conv, tool.TaskConvPrefix)
	day := p.now().In(p.loc).Format(time.DateOnly)
	msgs, aerr := p.alerts.Messages(ctx, "alert:grant:"+job+":"+day,
		fmt.Sprintf("Task %s tried to use %s, which its grant does not allow. The call was refused.", job, c.Name))
	if aerr != nil {
		p.log.Error("policy: grant alert not built", "task", job, "err", aerr)
	}
	return tool.Result{Out: msgs}, err
}

// Decide returns what to do with call c of tool s and the journal's name
// for the decision. The rules run in order and the first that applies
// wins (design section 4):
//
//  1. deeper than maxDepth: forbidden (R-AGT-4);
//  2. an admin-only tool for a non-admin: forbidden;
//  3. in a local-only session, a tool not declared LocalSafe: forbidden,
//     whatever a grant says (ADR-0049);
//  4. a forbidden tool: forbidden, whatever a grant says;
//  5. a scheduled task's call: outside its grant is out_of_grant, inside
//     it is auto, even in a tainted session (R-POL-8); web.fetch is
//     inside only on a granted host, message.send only to the task's own
//     conversation or its reply target;
//  6. memory.write and memory.forget: confirm in a tainted session, auto
//     otherwise (the memory guard still checks the entry), though they
//     are side effects;
//  7. any other side effect: confirm;
//  8. in a tainted session, a call with input to a tool that asks for it:
//     confirm (ADR-0049);
//  9. message.send to another conversation: confirm;
//  10. web.fetch of an address the owner or a search did not bring: confirm;
//  11. the tool's own policy.
//
// An argument a rule needs that does not parse makes the call confirm,
// never auto.
func Decide(maxDepth int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision) {
	for _, r := range rules {
		if pol, d, ok := r(maxDepth, s, c); ok {
			return pol, d
		}
	}
	return settle(s.Policy)
}

// rule returns a decision when it applies to the call.
type rule func(maxDepth int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool)

// rules are the rules of Decide, in order.
var rules = []rule{ruleDepth, ruleAdminOnly, ruleLocalOnly, ruleForbidden, ruleGrant, ruleMemoryWrite, ruleSideEffect, ruleTaintedInput, ruleMessageSend, ruleWebFetch}

// settle returns the tool's own policy with its journal decision; a policy
// that is none of the three fails closed.
func settle(p tool.Policy) (tool.Policy, journal.Decision) {
	switch p {
	case tool.PolicyAuto:
		return p, journal.DecisionAllowed
	case tool.PolicyConfirm:
		return p, journal.DecisionConfirm
	case tool.PolicyForbidden:
		return p, journal.DecisionForbidden
	default:
		return tool.PolicyForbidden, journal.DecisionForbidden
	}
}

func confirm() (tool.Policy, journal.Decision, bool) {
	return tool.PolicyConfirm, journal.DecisionConfirm, true
}

func forbidden() (tool.Policy, journal.Decision, bool) {
	return tool.PolicyForbidden, journal.DecisionForbidden, true
}

func ruleDepth(maxDepth int, _ tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if c.Depth > maxDepth {
		return forbidden()
	}
	return "", "", false
}

func ruleAdminOnly(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.AdminOnly && !c.Admin {
		return forbidden()
	}
	return "", "", false
}

// localRefused reports a call a local-only session must not make: deny by
// default, so a tool added later stays off until it says it is safe.
func localRefused(s tool.Spec, c tool.Call) bool { return c.LocalOnly && !s.LocalSafe }

func ruleLocalOnly(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if localRefused(s, c) {
		return forbidden()
	}
	return "", "", false
}

func ruleTaintedInput(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.AskTaintedInput && c.Tainted && hasInput(c) {
		return confirm()
	}
	return "", "", false
}

// hasInput reports whether the call gives an input. Only an absent or empty
// string counts as none: anything else, and arguments that do not parse, may
// carry the session's data.
func hasInput(c tool.Call) bool {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(c.Args, &args); err != nil {
		return true
	}
	raw, given := args["input"]
	return given && string(raw) != `""`
}

func ruleGrant(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if c.Grant == nil {
		return "", "", false
	}
	if !inGrant(*c.Grant, s, c) {
		return tool.PolicyForbidden, journal.DecisionOutOfGrant, true
	}
	return tool.PolicyAuto, journal.DecisionAllowed, true
}

// inGrant reports whether the grant covers the call: the tool, and for
// the tools in grantArgs their arguments.
func inGrant(g tool.Grant, s tool.Spec, c tool.Call) bool {
	if !slices.Contains(g.Tools, s.Name) {
		return false
	}
	check, ok := grantArgs[s.Name]
	return !ok || check(g, c)
}

// grantArgs check the arguments of granted tools that reach out: where
// web.fetch reads and where message.send writes. An argument that does
// not parse is outside the grant.
var grantArgs = map[string]func(g tool.Grant, c tool.Call) bool{
	toolWebFetch:    hostInGrant,
	toolMessageSend: convInGrant,
}

// hostInGrant reports whether web.fetch's address is on a granted host.
func hostInGrant(g tool.Grant, c tool.Call) bool {
	host, ok := hostOf(c)
	return ok && slices.ContainsFunc(g.Hosts, func(h string) bool { return strings.EqualFold(h, host) })
}

// convInGrant reports whether message.send goes where the task answers:
// no conv (its own), its own conversation, or the grant's reply target
// (R-POL-8); a task cannot send its findings anywhere else.
func convInGrant(g tool.Grant, c tool.Call) bool {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(c.Args, &args); err != nil || args == nil {
		return false
	}
	raw, given := args["conv"]
	if !given {
		return true
	}
	var conv string
	if err := json.Unmarshal(raw, &conv); err != nil {
		return false
	}
	return conv == c.Conv || conv == g.ReplyTo
}

// hostOf is the host (with a non-default port) of the call's normalised
// "url" argument.
func hostOf(c tool.Call) (string, bool) {
	raw, ok := stringArg(c, "url")
	if !ok {
		return "", false
	}
	norm, ok := urlnorm.Normalize(raw)
	if !ok {
		return "", false
	}
	u, err := url.Parse(norm)
	if err != nil {
		return "", false
	}
	return u.Host, true
}

// stringArg reads the string argument name of the call.
func stringArg(c tool.Call, name string) (string, bool) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(c.Args, &args); err != nil {
		return "", false
	}
	var v string
	if err := json.Unmarshal(args[name], &v); err != nil {
		return "", false
	}
	return v, true
}

func ruleForbidden(_ int, s tool.Spec, _ tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.Policy == tool.PolicyForbidden {
		return forbidden()
	}
	return "", "", false
}

func ruleSideEffect(_ int, s tool.Spec, _ tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.Class == tool.ClassSideEffect {
		return confirm()
	}
	return "", "", false
}

func ruleMemoryWrite(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.Name != toolMemoryWrite && s.Name != toolMemoryForget {
		return "", "", false
	}
	if c.Tainted {
		return confirm()
	}
	return tool.PolicyAuto, journal.DecisionAllowed, true
}

func ruleMessageSend(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.Name != toolMessageSend {
		return "", "", false
	}
	if conv, ok := stringArg(c, "conv"); !ok || conv != c.Conv {
		return confirm()
	}
	return "", "", false
}

func ruleWebFetch(_ int, s tool.Spec, c tool.Call) (tool.Policy, journal.Decision, bool) {
	if s.Name != toolWebFetch {
		return "", "", false
	}
	if raw, ok := stringArg(c, "url"); !ok || c.KnownURL == nil || !c.KnownURL(raw) {
		return confirm()
	}
	return "", "", false
}
