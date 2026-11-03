package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/router"
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
type SystemJob func(ctx context.Context) (string, error)

// Deps are the agent's collaborators. All are required except Middleware,
// QuickActions, SystemJobs, Facts, Now and Log; the composition root
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
	Now   func() time.Time
	Log   *slog.Logger
}

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
		return nil, queue.StateUpdate{}, err
	}
	if err := runStages(ctx, t, a.stages); err != nil {
		return nil, queue.StateUpdate{}, err
	}
	return a.finish(ctx, t)
}

// payloads decode the payload of each kind of event the agent handles
// into the turn; false means the event is not one to answer.
var payloads = map[event.Kind]func(t *turn) bool{
	event.KindMessage: func(t *turn) bool {
		// A message with only files is answered too: nothing is dropped
		// silently (R-DLV-10).
		return json.Unmarshal(t.event.Payload, &t.msg) == nil && (strings.TrimSpace(t.msg.Text) != "" || len(t.msg.Files) > 0)
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
	t.record = turns.Turn{EventID: e.ID, Conv: e.Conv, Principal: t.msg.From, Started: t.now, Outcome: turns.OutcomeOK}
	return t, true, nil
}

// finish logs and records the turn and encodes the new state.
func (a *Agent) finish(ctx context.Context, t *turn) ([]event.Outgoing, queue.StateUpdate, error) {
	t.record.Finished = a.d.Now()
	a.logTurn(ctx, &t.record)
	// The turn's context may have run out during compaction; the trace
	// still counts for the budget and the limits.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.d.Turns.Record(rctx, t.record); err != nil {
		a.log.Warn("agent: trace not recorded", "id", t.event.ID, "err", err)
	}
	data, err := json.Marshal(t.state)
	if err != nil {
		return nil, queue.StateUpdate{}, err
	}
	return t.out, queue.StateUpdate{Conv: t.event.Conv, Data: data}, nil
}
