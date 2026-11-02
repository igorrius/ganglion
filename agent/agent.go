package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
)

// StateReader returns a conversation's stored state (the queue).
type StateReader interface {
	State(ctx context.Context, conv string) (json.RawMessage, error)
}

// PersonaFunc returns the operator's description of the agent; it is read
// every turn so edits apply at once. "" means DefaultPersona.
type PersonaFunc func() (string, error)

// Deps are the agent's collaborators. All are required except Middleware,
// Now and Log; the composition root passes no-op implementations where a part is
// off (ADR-0028).
type Deps struct {
	States     StateReader
	Router     router.Router
	Providers  map[string]provider.Provider
	Tools      *tool.Registry
	Middleware []tool.Middleware // around every tool call, after tracing; the first runs first
	IsAdmin    AdminFunc
	Turns      *turns.Log
	Archive    SessionStore
	Alerts     alert.Admins // who hears about a reached daily limit
	Persona    PersonaFunc
	Now        func() time.Time
	Log        *slog.Logger
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
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	a := &Agent{cfg: cfg, d: d, log: d.Log, in: newInstruments()}
	a.stages = []stage{a.idle, a.command, a.limits, a.answer, a.compactHistory}
	a.commands = map[string]command{
		"/new": a.newSession, "/reset": a.newSession,
		"/sessions": a.listSessions, "/resume": a.resume,
	}
	a.tool = tool.Chain(d.Tools.Run, append([]tool.Middleware{a.traced}, d.Middleware...)...)
	return a
}

// Handle implements core.Handler: it runs the turn's stages and returns
// the replies and the new state. Events that are not text messages are
// left alone.
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

// begin decodes the message and loads the conversation's state. ok is
// false for an event the agent does not answer.
func (a *Agent) begin(ctx context.Context, e event.Event) (*turn, bool, error) {
	if e.Kind != event.KindMessage {
		return nil, false, nil
	}
	var m event.Message
	if err := json.Unmarshal(e.Payload, &m); err != nil || strings.TrimSpace(m.Text) == "" {
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
	now := a.d.Now()
	return &turn{event: e, msg: m, state: st, now: now, record: turns.Turn{
		EventID: e.ID, Conv: e.Conv, Principal: m.From, Started: now, Outcome: turns.OutcomeOK,
	}}, true, nil
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
