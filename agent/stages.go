package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
)

// idle starts a new session after a long silence, archiving the old one.
func (a *Agent) idle(ctx context.Context, t *turn) (bool, error) {
	st := t.state
	if a.cfg.SessionIdle <= 0 || len(st.Messages) == 0 || st.Last.IsZero() || t.now.Sub(st.Last) <= a.cfg.SessionIdle {
		return false, nil
	}
	if err := a.archive(ctx, t.event.Conv, st, "idle"); err != nil {
		return false, err
	}
	a.log.Info("agent: new session after silence", "conv", t.event.Conv, "silent_for", t.now.Sub(st.Last).Round(time.Minute))
	t.state = State{V: StateVersion, Session: t.event.ID, Started: t.now, Last: t.now}
	return false, nil
}

// limits refuses the message when its sender is over a daily limit, and
// tells the admins.
func (a *Agent) limits(ctx context.Context, t *turn) (bool, error) {
	alerts, over := a.overLimit(ctx, t.now, t.msg.From, t.event.ID)
	if !over {
		return false, nil
	}
	t.record.Outcome = turns.OutcomeDailyLimit
	t.reply(replyDailyLimit)
	t.out = append(t.out, alerts...)
	return true, nil
}

// answer asks the models for an answer to the person's message, running
// tools in between, and adds the exchange to the history.
func (a *Agent) answer(ctx context.Context, t *turn) (bool, error) {
	if err := a.setAdmin(ctx, t); err != nil {
		return false, err
	}
	user := provider.Message{Role: provider.RoleUser, Content: speaker(t.msg) + ": " + t.msg.Text}
	return a.converse(ctx, t, user)
}

// setAdmin looks up whether the turn's principal may use tools.
func (a *Agent) setAdmin(ctx context.Context, t *turn) error {
	admin, err := a.d.IsAdmin(ctx, t.msg.From)
	if err != nil {
		return fmt.Errorf("agent: role of %s: %w", t.msg.From, err)
	}
	t.admin = admin
	return nil
}

// converse adds user to the history and runs the model on it, with tools
// in between, and replies with its answer. done is true when no model
// answered.
func (a *Agent) converse(ctx context.Context, t *turn, user provider.Message) (bool, error) {
	if t.state.Session == "" {
		t.state.Session, t.state.Started = t.event.ID, t.now
	}
	t.known = knownURLs(append(slices.Clone(t.state.Messages), user))
	turnCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	defer cancel()
	added, text, ok, err := a.steps(ctx, turnCtx, t, append(a.prompt(t.state), user))
	if err != nil {
		return false, err
	}
	if !ok {
		t.record.Outcome = turns.OutcomeNoModel
		t.state.Messages, t.state.Last = append(t.state.Messages, user), t.now
		t.reply(replyNoModel)
		return true, nil
	}
	if text == "" {
		t.record.Outcome = turns.OutcomeStepLimit
		text = lastText(added)
	}
	t.state.Messages = append(append(t.state.Messages, user), added...)
	t.state.Last = t.now
	t.reply(text)
	return false, nil
}

// steps runs model calls and tools until the model answers without tool
// calls or MaxSteps run out. It returns the messages the turn added after
// the user's and the answer, "" at the step limit. ok is false when no
// model answered a step. A cancelled ctx (shutdown) is an error, so the
// event is offered again.
func (a *Agent) steps(ctx, turnCtx context.Context, t *turn, msgs []provider.Message) (added []provider.Message, text string, ok bool, err error) {
	tools := a.toolSpecs(t)
	task := router.TaskReq{Kind: router.TaskMessage, Tools: len(tools) > 0}
	for t.record.Steps < a.cfg.MaxSteps {
		t.record.Steps++
		resp, ok := a.call(turnCtx, task, msgs, tools, &t.record)
		if ctx.Err() != nil {
			return nil, "", false, ctx.Err()
		}
		if !ok {
			return nil, "", false, nil
		}
		msg := resp.Message
		msg.Role = provider.RoleAssistant
		msgs, added = append(msgs, msg), append(added, msg)
		if len(msg.ToolCalls) == 0 {
			return added, msg.Content, true, nil
		}
		results := a.runTools(turnCtx, t, msg.ToolCalls)
		msgs, added = append(msgs, results...), append(added, results...)
	}
	return added, "", true, nil
}

// lastText is the newest text the model wrote, for a turn cut by the step
// limit; replyStepLimit when it wrote none.
func lastText(added []provider.Message) string {
	for _, m := range slices.Backward(added) {
		if m.Role == provider.RoleAssistant && m.Content != "" {
			return m.Content
		}
	}
	return replyStepLimit
}

// compactHistory folds old history into the summary once it grows past
// HistoryTokens.
func (a *Agent) compactHistory(ctx context.Context, t *turn) (bool, error) {
	if t.state.tokens() <= a.cfg.HistoryTokens {
		return false, nil
	}
	cctx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	defer cancel()
	t.state = a.compact(cctx, t.state, &t.record)
	return false, nil
}

// prompt is the system prompt, the summary and the kept history.
func (a *Agent) prompt(st State) []provider.Message {
	msgs := []provider.Message{{Role: provider.RoleSystem, Content: a.systemPrompt()}}
	if st.Summary != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: summaryPrefix + st.Summary})
	}
	return append(msgs, st.Messages...)
}

// systemPrompt is the core's rules followed by the persona.
func (a *Agent) systemPrompt() string {
	persona, err := a.d.Persona()
	if err != nil {
		a.log.Warn("agent: persona unreadable; using the default", "err", err)
		persona = ""
	}
	if persona = strings.TrimSpace(persona); persona == "" {
		persona = DefaultPersona
	}
	return CoreRules + "\n\n" + persona
}

func speaker(m event.Message) string {
	if m.Name != "" {
		return m.Name
	}
	return m.From
}
