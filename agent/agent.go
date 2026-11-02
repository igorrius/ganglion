package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
)

// Config tunes the agent. Zero values take the defaults; a negative daily
// limit turns that limit off.
type Config struct {
	SystemPrompt  string        // default DefaultSystemPrompt
	MaxSteps      int           // model calls per turn; default 8
	TurnTimeout   time.Duration // default 2m
	CallTimeout   time.Duration // per model call; default 60s
	HistoryTokens int           // compact above this estimate; default 6000
	KeepRecent    int           // messages kept verbatim at compaction; default 12
	DailyTurns    int           // per principal; default 300
	DailyTokens   int           // per principal; default 1,000,000
	SessionIdle   time.Duration // a new session after this much silence; default 6h; negative = off
	Location      *time.Location
}

func (c Config) withDefaults() Config {
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	defD := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	if c.SystemPrompt == "" {
		c.SystemPrompt = DefaultSystemPrompt
	}
	def(&c.MaxSteps, 8)
	defD(&c.TurnTimeout, 2*time.Minute)
	defD(&c.CallTimeout, 60*time.Second)
	def(&c.HistoryTokens, 6000)
	def(&c.KeepRecent, 12)
	def(&c.DailyTurns, 300)
	def(&c.DailyTokens, 1_000_000)
	defD(&c.SessionIdle, 6*time.Hour)
	if c.Location == nil {
		c.Location = time.UTC
	}
	return c
}

// Deps are the agent's collaborators.
type Deps struct {
	States interface {
		State(ctx context.Context, conv string) (json.RawMessage, error)
	} // the queue
	Router    router.Router
	Providers map[string]provider.Provider
	Tools     Tools
	Turns     *turns.Log
	Archive   *Archive // nil: sessions are not archived
	Admins    func(ctx context.Context) ([]string, error)
	AlertConv func(principal string) (transport, conv string, ok bool) // a principal's private chat
	Now       func() time.Time
	Log       *slog.Logger
}

// Agent handles conversation turns; Handle is a core.Handler.
type Agent struct {
	cfg Config
	d   Deps
	log *slog.Logger
}

// New returns an agent.
func New(cfg Config, d Deps) *Agent {
	if d.Tools == nil {
		d.Tools = NoTools{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Agent{cfg: cfg.withDefaults(), d: d, log: d.Log}
}

// turnRecord accumulates the trace of one turn.
type turnRecord struct{ turns.Turn }

func (tr *turnRecord) add(c router.Candidate, u provider.Usage) {
	tr.InputTokens += u.InputTokens
	tr.CacheReadTokens += u.CacheReadTokens
	tr.CacheWriteTokens += u.CacheWriteTokens
	tr.OutputTokens += u.OutputTokens
	tr.CostUSD += (float64(u.InputTokens)*c.PriceInMTok + float64(u.OutputTokens)*c.PriceOutMTok) / 1e6
}

func errorKind(err error) string {
	if e, ok := errors.AsType[*provider.Error](err); ok {
		return string(e.Kind)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(provider.KindTimeout)
	}
	return "error"
}

// call routes task and tries the candidates in order until one answers.
// Any error moves to the next candidate (spec 7.3, layer 3); a cancelled
// ctx stops. ok is false when no candidate answered.
func (a *Agent) call(ctx context.Context, task router.TaskReq, msgs []provider.Message, tools []provider.Tool, tr *turnRecord) (provider.ChatResponse, bool) {
	cands, err := a.d.Router.Route(ctx, task)
	if err != nil {
		a.log.Error("agent: no model fits", "conv", tr.Conv, "kind", task.Kind, "err", err)
		return provider.ChatResponse{}, false
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			return provider.ChatResponse{}, false
		}
		p, ok := a.d.Providers[c.Provider]
		if !ok {
			tr.Route = append(tr.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: "no_provider"})
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, a.cfg.CallTimeout)
		resp, err := p.Chat(callCtx, provider.ChatRequest{Model: c.Model, Messages: msgs, Tools: tools})
		cancel()
		if err == nil && strings.TrimSpace(resp.Message.Content) == "" && len(resp.Message.ToolCalls) == 0 {
			err = &provider.Error{Provider: c.Provider, Kind: provider.KindUnavailable, Msg: "empty answer"}
		}
		a.d.Router.Report(c, err)
		if err == nil {
			tr.Route = append(tr.Route, turns.Attempt{Provider: c.Provider, Model: c.Model})
			tr.add(c, resp.Usage)
			return resp, true
		}
		tr.Route = append(tr.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: errorKind(err)})
		attrs := []any{"conv", tr.Conv, "provider", c.Provider, "model", c.Model, "kind", errorKind(err)}
		if pe, ok := errors.AsType[*provider.Error](err); ok {
			attrs = append(attrs, "status", pe.Status, "msg", pe.Msg) // provider text, keys redacted; never the prompt
		}
		a.log.Warn("agent: model call failed", attrs...)
	}
	return provider.ChatResponse{}, false
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// Handle implements core.Handler.
func (a *Agent) Handle(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
	if e.Kind != "message" {
		return nil, queue.StateUpdate{}, nil
	}
	var m event.Message
	if err := json.Unmarshal(e.Payload, &m); err != nil || strings.TrimSpace(m.Text) == "" {
		return nil, queue.StateUpdate{}, nil
	}
	raw, err := a.d.States.State(ctx, e.Conv)
	if err != nil {
		return nil, queue.StateUpdate{}, err
	}
	st, err := DecodeState(raw)
	if err != nil {
		a.log.Error("agent: unreadable state; starting a new session", "conv", e.Conv, "err", err)
		st = State{V: StateVersion}
	}
	now := a.d.Now()
	tr := &turnRecord{turns.Turn{EventID: e.ID, Conv: e.Conv, Principal: m.From, Started: now, Outcome: "ok"}}
	reply := func(text string) event.Outgoing {
		return event.Outgoing{IdemKey: e.IdemKey + ":reply:0", Transport: e.Source, Conv: e.Conv, Text: text}
	}
	finish := func(out []event.Outgoing, st State) ([]event.Outgoing, queue.StateUpdate, error) {
		tr.Finished = a.d.Now()
		// The turn's context may have run out during compaction; the trace
		// still counts for the budget and the limits.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rcancel()
		if err := a.d.Turns.Record(rctx, tr.Turn); err != nil {
			a.log.Warn("agent: trace not recorded", "id", e.ID, "err", err)
		}
		data, err := json.Marshal(st)
		if err != nil {
			return nil, queue.StateUpdate{}, err
		}
		return out, queue.StateUpdate{Conv: e.Conv, Data: data}, nil
	}

	st, out, handled, err := a.sessions(ctx, e, st, m, now, tr, reply)
	if err != nil {
		return nil, queue.StateUpdate{}, err
	}
	if handled {
		return finish(out, st)
	}

	if alerts, over := a.overLimit(ctx, now, m.From, e.ID); over {
		tr.Outcome = "daily_limit"
		return finish(append([]event.Outgoing{reply(replyDailyLimit)}, alerts...), st)
	}

	if st.Session == "" {
		st.Session, st.Started = e.ID, now
	}
	user := provider.Message{Role: "user", Content: speaker(m) + ": " + m.Text}
	msgs := []provider.Message{{Role: "system", Content: a.cfg.SystemPrompt}}
	if st.Summary != "" {
		msgs = append(msgs, provider.Message{Role: "system", Content: summaryPrefix + st.Summary})
	}
	msgs = append(msgs, st.Messages...)
	msgs = append(msgs, user)
	added := []provider.Message{user}

	turnCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	defer cancel()
	tools := a.d.Tools.List()
	answer, lastText := "", ""
	for tr.Steps < a.cfg.MaxSteps {
		tr.Steps++
		resp, ok := a.call(turnCtx, router.TaskReq{Kind: "message", Tools: len(tools) > 0}, msgs, tools, tr)
		if ctx.Err() != nil {
			return nil, queue.StateUpdate{}, ctx.Err() // shutting down: the event is offered again
		}
		if !ok {
			tr.Outcome = "no_model"
			st.Messages, st.Last = append(st.Messages, user), now
			return finish([]event.Outgoing{reply(replyNoModel)}, st)
		}
		msg := resp.Message
		msg.Role = "assistant"
		msgs, added = append(msgs, msg), append(added, msg)
		if msg.Content != "" {
			lastText = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			answer = msg.Content
			break
		}
		for _, call := range msg.ToolCalls {
			started := time.Now()
			result, err := a.d.Tools.Call(turnCtx, call)
			if err != nil {
				result = "error: " + err.Error()
			}
			tr.Tools = append(tr.Tools, turns.ToolUse{Name: call.Name, OK: err == nil, Ms: time.Since(started).Milliseconds()})
			res := provider.Message{Role: "tool", ToolCallID: call.ID, Content: result}
			msgs, added = append(msgs, res), append(added, res)
		}
	}
	if answer == "" {
		tr.Outcome = "step_limit"
		answer = lastText
		if answer == "" {
			answer = replyStepLimit
		}
	}

	st.Messages, st.Last = append(st.Messages, added...), now
	if st.tokens() > a.cfg.HistoryTokens {
		cctx, ccancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
		st = a.compact(cctx, st, tr)
		ccancel()
	}
	return finish([]event.Outgoing{reply(answer)}, st)
}

func speaker(m event.Message) string {
	if m.Name != "" {
		return m.Name
	}
	return m.From
}

// overLimit checks the principal's daily turns and tokens. Over a limit it
// returns one alert per admin; their keys repeat for the day, so the outbox
// sends each alert once.
func (a *Agent) overLimit(ctx context.Context, now time.Time, principal, eventID string) ([]event.Outgoing, bool) {
	if a.cfg.DailyTurns < 0 && a.cfg.DailyTokens < 0 {
		return nil, false
	}
	day := dayStart(now, a.cfg.Location)
	tot, err := a.d.Turns.Totals(ctx, day, principal, eventID)
	if err != nil {
		a.log.Warn("agent: daily totals unavailable; limit not checked", "err", err)
		return nil, false
	}
	if (a.cfg.DailyTurns < 0 || tot.Turns < a.cfg.DailyTurns) && (a.cfg.DailyTokens < 0 || tot.Tokens < a.cfg.DailyTokens) {
		return nil, false
	}
	a.log.Warn("agent: daily limit reached", "principal", principal, "turns", tot.Turns, "tokens", tot.Tokens)
	var alerts []event.Outgoing
	if a.d.Admins != nil && a.d.AlertConv != nil {
		admins, _ := a.d.Admins(ctx)
		for _, adm := range admins {
			transport, conv, ok := a.d.AlertConv(adm)
			if !ok {
				continue
			}
			alerts = append(alerts, event.Outgoing{
				IdemKey:   "alert:daily-limit:" + day.Format("2006-01-02") + ":" + principal + ":" + adm,
				Transport: transport, Conv: conv,
				Text: "Daily limit reached for " + principal + " (turns or tokens); the bot refuses them until tomorrow.",
			})
		}
	}
	return alerts, true
}
