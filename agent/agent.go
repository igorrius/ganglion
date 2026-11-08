package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/skills"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

// StateReader returns a conversation's stored state (the queue).
type StateReader interface {
	State(ctx context.Context, conv string) (json.RawMessage, error)
}

// PersonaFunc returns the operator's description of the agent; it is read
// every turn so edits apply at once. "" means DefaultPersona.
type PersonaFunc func() (string, error)

// FactsFunc returns the known-facts block of the memory for the system
// prompt (memory.Memory.Index); "" when nothing is stored. It must give
// the same text for the same memory, so the prompt's cache keeps
// hitting.
type FactsFunc func() string

// NoFacts is the FactsFunc of an agent without memory.
func NoFacts() string { return "" }

// NameFunc returns the name a principal is listed under
// (access.List.Name); "" when none.
type NameFunc func(ctx context.Context, principal string) (string, error)

// SystemJob is the code of a system job (schedule.KindSystem), such as
// the memory audit: it runs without a model and returns the report sent
// to the job's conversation (ruling R36). An error leaves the event to be
// handled again.
type SystemJob func(ctx context.Context, sp texts.Speaker) (string, error)

// Deps are the agent's collaborators. All are required except Middleware,
// QuickActions, SystemJobs, Facts, Inbox, Plans, Status, Now and Log; the composition root
// passes no-op implementations where a part is off (ADR-0028).
type Deps struct {
	States     StateReader
	Router     router.Router
	Providers  map[string]provider.Provider
	Tools      *tool.Registry
	Middleware []tool.Middleware // around every tool call, after tracing; the first runs first
	// Policy decides the calls the model makes, inside Middleware. A call
	// the owner confirmed runs without it: it was decided (ADR-0032).
	Policy  tool.Middleware
	Pending Confirmations // calls awaiting the owner and their buttons
	// Outbox takes a press's acknowledgement before the pressed action
	// runs (ruling R40).
	Outbox Enqueuer
	// Inbox shows what waits behind a turn, so a message can join it
	// (R-LIVE-1); NoInbox when unset.
	Inbox Inbox
	// Sent says where a delivered message went, so an expired question can
	// close itself.
	Sent SentRefs
	// QuickActions run quick-action buttons by their action name; tools
	// register them (schedule.details, schedule.delete, schedule.run_now).
	QuickActions map[string]pending.QuickAction
	Jobs         Jobs // scheduled jobs, read when their time comes
	// SystemJobs are the system jobs' code by job id (builtin.AuditJobID).
	SystemJobs map[string]SystemJob
	IsAdmin    AdminFunc
	// Names are the access list's names, for the line that tells a task
	// run who receives its answer (ruling R41).
	Names   NameFunc
	Turns   *turns.Log
	Archive SessionStore
	Alerts  alert.Admins // who hears about a reached daily limit
	Persona PersonaFunc
	// Facts is the memory's index, shown to admins only (design section
	// 4, rule 8); NoFacts when unset.
	Facts FactsFunc
	// Modules updates and rolls back the node's modules for /modules
	// (R-RUN-8); NoModules when unset.
	Modules Modules
	// Skills are listed in an admin's system prompt, and the one the
	// model reads routes the rest of the turn (R-RTR-4); none when unset.
	Skills Skills
	// Plans closes or keeps a conversation's plan at the end of a turn
	// (R-LIVE-8); NoPlans when unset.
	Plans Plans
	// Languages knows each person's language (R-LANG-1); NoLanguages when unset.
	Languages Languages
	// Translate says a person is being written to in a language, which may need translating
	// (R-LANG-5). It must not block; nothing happens when unset.
	Translate func(texts.Lang)
	// Texts are the fixed texts a person reads (R-LANG-2); the shipped files when unset.
	Texts *texts.Catalog
	// Status hears the stages of a person's message (R-LIVE-7); NoStatus when
	// unset. It must not block.
	Status StatusFunc
	// StatusSources are the components /status asks (R-OBS-11); none when unset.
	StatusSources []status.Source
	Now           func() time.Time
	Log           *slog.Logger
}

// Languages is what the agent needs of a person's language (language.Store).
type Languages interface {
	Seen(ctx context.Context, principal, code string) error
	Resolve(ctx context.Context, principal string) (texts.Lang, error)
	// Chosen is the language a person asked for, if they did.
	Chosen(ctx context.Context, principal string) (texts.Lang, bool, error)
	// NoticeFor reports whether this event's message tells principal that l's texts are a machine's.
	NoticeFor(ctx context.Context, principal string, l texts.Lang, event string) (bool, error)
}

// NoLanguages is the Languages of an agent that knows none: everyone reads English.
type NoLanguages struct{}

// Seen implements Languages.
func (NoLanguages) Seen(context.Context, string, string) error { return nil }

// Resolve implements Languages.
func (NoLanguages) Resolve(context.Context, string) (texts.Lang, error) { return texts.English, nil }

// Chosen implements Languages.
func (NoLanguages) Chosen(context.Context, string) (texts.Lang, bool, error) { return "", false, nil }

// NoticeFor implements Languages.
func (NoLanguages) NoticeFor(context.Context, string, texts.Lang, string) (bool, error) {
	return false, nil
}

// Plans is the end of a turn for a conversation's plan (plan.Store).
type Plans interface {
	Finish(ctx context.Context, conv string, kept bool) error
}

// NoPlans is the Plans of an agent with no plan tools.
type NoPlans struct{}

// Finish implements Plans.
func (NoPlans) Finish(context.Context, string, bool) error { return nil }

// StatusFunc hears where the work on a person's message stands.
type StatusFunc func(event.Status)

// NoStatus is the StatusFunc of a node that shows no status.
func NoStatus(event.Status) {}

// Agent handles conversation turns; Handle is a core.Handler.
type Agent struct {
	cfg      Config
	d        Deps
	log      *slog.Logger
	in       instruments
	stages   []stage
	commands map[string]command
	tool     tool.Func
}

// New returns an agent.
func New(cfg Config, d Deps) *Agent {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Facts == nil {
		d.Facts = NoFacts
	}
	if d.Languages == nil {
		d.Languages = NoLanguages{}
	}
	if d.Translate == nil {
		d.Translate = func(texts.Lang) {}
	}
	if d.Texts == nil {
		d.Texts = texts.Shipped()
	}
	if d.Modules == nil {
		d.Modules = NoModules{}
	}
	if d.Inbox == nil {
		d.Inbox = NoInbox{}
	}
	if d.Plans == nil {
		d.Plans = NoPlans{}
	}
	if d.Status == nil {
		d.Status = NoStatus
	}
	if d.Skills == nil {
		d.Skills = skills.Set{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	a := &Agent{cfg: cfg, d: d, log: d.Log, in: newInstruments()}
	a.stages = []stage{a.resume, a.scheduled, a.dropStalePending, a.idle, a.command, a.limits, a.answer, a.compactHistory}
	a.commands = map[string]command{}
	for _, c := range a.commandTable() {
		a.commands[c.word] = c.run
	}
	a.tool = tool.Chain(d.Tools.Run, slices.Concat([]tool.Middleware{a.traced}, d.Middleware, []tool.Middleware{d.Policy})...)
	return a
}

// Handle implements core.Handler: it runs the turn's stages and returns
// the replies and the new state. It answers text messages, button
// presses, the expiry of pending calls and scheduled jobs; other events
// are left alone.
func (a *Agent) Handle(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
	t, ok, err := a.begin(ctx, e)
	if err != nil || !ok {
		a.endWithoutTurn(e)
		return nil, queue.StateUpdate{}, err
	}
	defer a.reportDone(t)
	ctx = event.WithOrigin(ctx, t.origin)
	if err := runStages(ctx, t, a.stages); err != nil {
		return nil, queue.StateUpdate{}, err
	}
	return a.finish(ctx, t)
}

// payloads decode the payload of each kind of event the agent handles
// into the turn; false means the event is not one to answer.
var payloads = map[event.Kind]func(t *turn) bool{
	event.KindMessage: func(t *turn) bool {
		m, ok := decodeMessage(t.event.Payload)
		t.msg = m
		return ok
	},
	event.KindDecision: func(t *turn) bool {
		d := &t.decision
		if json.Unmarshal(t.event.Payload, d) != nil || d.Button == "" || d.From == "" {
			return false
		}
		t.msg = event.Message{From: d.From, Name: d.Name, Ref: d.Ref}
		return true
	},
	event.KindToolResult: func(t *turn) bool {
		return json.Unmarshal(t.event.Payload, &t.toolResult) == nil && t.toolResult.Pending != ""
	},
	event.KindSchedule: func(t *turn) bool {
		return json.Unmarshal(t.event.Payload, &t.schedule) == nil && t.schedule.Job != ""
	},
}

// begin decodes the event and loads the conversation's state. ok is
// false for an event the agent does not answer.
func (a *Agent) begin(ctx context.Context, e event.Event) (*turn, bool, error) {
	decode, ok := payloads[e.Kind]
	t := &turn{event: e}
	if !ok || !decode(t) {
		return nil, false, nil
	}
	raw, err := a.d.States.State(ctx, e.Conv)
	if err != nil {
		return nil, false, err
	}
	st, err := DecodeState(raw)
	if err != nil {
		a.log.Error("agent: unreadable state; starting a new session", "conv", e.Conv, "err", err)
		st = State{V: StateVersion}
	}
	t.state, t.now = st, a.d.Now()
	t.transport, t.replyConv = e.Source, e.Conv
	a.noteLanguage(ctx, t)
	t.refs = []string{t.msg.Ref}
	t.record = turns.Turn{EventID: e.ID, Conv: e.Conv, Principal: t.msg.From, Started: t.now, Outcome: turns.OutcomeOK}
	return t, true, nil
}

// finish logs and records the turn and encodes the new state.
func (a *Agent) finish(ctx context.Context, t *turn) ([]event.Outgoing, queue.StateUpdate, error) {
	t.record.Finished, t.record.Joined = a.d.Now(), len(t.taken)
	a.logTurn(ctx, &t.record, len(t.taken))
	// The turn's context may have run out during compaction; the trace
	// still counts for the budget and the limits.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.d.Turns.Record(rctx, t.record); err != nil {
		a.log.Warn("agent: trace not recorded", "id", t.event.ID, "err", err)
	}
	// A parked call goes on after its press, and a command in the background
	// after its end (R-CMD-4), and the plan with them. A plan is progress, so
	// a failure here costs the turn nothing.
	if err := a.d.Plans.Finish(rctx, t.event.Conv, len(t.state.Pending) > 0 || len(t.state.Commands) > 0); err != nil {
		a.log.Warn("agent: plan not finished", "conv", t.event.Conv, "err", err)
	}
	a.tellMachineTexts(rctx, t)
	data, err := json.Marshal(t.state)
	if err != nil {
		return nil, queue.StateUpdate{}, err
	}
	stamp(t.out, t.origin())
	return t.out, queue.StateUpdate{Conv: t.event.Conv, Data: data, Taken: t.taken}, nil
}
