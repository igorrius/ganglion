package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
)

const (
	// maxAdditionsAtOnce is how many messages one boundary takes; the rest
	// wait for the next boundary or their own turn.
	maxAdditionsAtOnce = 8
	// minAdditionTime is the time a turn must have left to take one: an
	// answer to it takes a model call, and the clock is not extended
	// (R-LIVE-6).
	minAdditionTime = 30 * time.Second
	// skippedResult answers a call that did not run because the person
	// wrote again (R-LIVE-2): every call keeps a result, so the history
	// stays valid, and the model decides again with the addition in view.
	skippedResult = "not run: the person sent a new message; decide again"
)

// errAdditionWaiting is the cause a CLI run is ended with when an addition
// waits (R-LIVE-3), and the error Exec returns for a call it refuses for that
// reason. It is not a failure: no fallback, no retry.
var errAdditionWaiting = errors.New("the person sent a new message; this run ends")

// errNoRoomForAddition refuses a CLI run's call while a message waits that the
// turn has no step or time left to take (R-LIVE-2): the run goes on, so its
// answer survives, and the message has its own turn after it.
var errNoRoomForAddition = errors.New("not run: the person sent a new message; answer with what you have")

// outcomeEnded is call's result for a run ended for an addition. It is never
// recorded: the turn goes on.
const outcomeEnded turns.Outcome = "ended_for_addition"

// Inbox shows what waits behind a turn's event (queue.Queue.Behind).
type Inbox interface {
	Behind(ctx context.Context, e event.Event, limit int) ([]event.Event, error)
}

// NoInbox is the Inbox of an agent with no queue: nothing waits.
type NoInbox struct{}

// Behind implements Inbox.
func (NoInbox) Behind(context.Context, event.Event, int) ([]event.Event, error) { return nil, nil }

// additionMsg is a message that may join the running turn.
type additionMsg struct {
	id  string
	msg event.Message
}

// waiting returns the additions the turn can take now: those behind it, while
// it has steps and time left (R-LIVE-6).
func (a *Agent) waiting(ctx context.Context, t *turn) []additionMsg {
	if !a.hasRoom(t) {
		return nil
	}
	return a.behind(ctx, t)
}

// behind returns the unbroken run of events behind the turn that qualify as
// additions (R-LIVE-1): it stops at the first that does not, so a later
// message never overtakes an earlier one. No tool call starts while it holds
// any, whether or not the turn can take them (R-LIVE-2). It changes nothing,
// and taking is join's. A read that fails is logged and means nothing waits:
// the messages keep their own turns. It reads only what the turn's loop writes
// between model calls, so a CLI provider's goroutines may call it.
func (a *Agent) behind(ctx context.Context, t *turn) []additionMsg {
	if t.event.Kind != event.KindMessage || t.grant != nil || t.noTools {
		return nil
	}
	// Taken events stay pending until Complete, so they are read again.
	behind, err := a.d.Inbox.Behind(ctx, t.event, len(t.taken)+maxAdditionsAtOnce)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Warn("agent: inbox not read", "conv", t.event.Conv, "err", err)
			a.in.inboxErrors.Add(ctx, 1)
		}
		return nil
	}
	var out []additionMsg
	for _, e := range behind {
		if slices.Contains(t.taken, e.ID) {
			continue
		}
		m, ok := a.addition(ctx, t, e)
		if !ok {
			break
		}
		out = append(out, additionMsg{id: e.ID, msg: m})
	}
	return out
}

// hasRoom reports whether the turn has the steps and the time to answer an
// addition (R-LIVE-6). The deadline is the context's, so it is read on the real
// clock, as the context does.
func (a *Agent) hasRoom(t *turn) bool {
	return t.record.Steps < a.cfg.MaxSteps && time.Until(t.deadline) >= minAdditionTime
}

// addition decodes e and reports whether it qualifies: a message of the same
// conversation from the same person, not a command, not expired, and within
// the daily limits. An alert about a limit belongs to the message's own turn.
func (a *Agent) addition(ctx context.Context, t *turn, e event.Event) (event.Message, bool) {
	now := a.d.Now()
	if e.Kind != event.KindMessage || e.Conv != t.event.Conv || e.Expired(now) {
		return event.Message{}, false
	}
	m, ok := decodeMessage(e.Payload)
	if !ok || m.From != t.msg.From {
		return event.Message{}, false
	}
	if _, _, isCommand := a.commandIn(m); isCommand || a.over(ctx, now, m.From, e.ID) {
		return event.Message{}, false
	}
	return m, true
}

// decodeMessage decodes a message's payload; false for one with nothing to
// answer. A message with only files is answered too: nothing is dropped
// silently (R-DLV-10).
func decodeMessage(payload json.RawMessage) (event.Message, bool) {
	var m event.Message
	if json.Unmarshal(payload, &m) != nil {
		return event.Message{}, false
	}
	return m, strings.TrimSpace(m.Text) != "" || len(m.Files) > 0
}

// join adds the additions to the turn as a turn's first message is added
// (R-LIVE-4): the speaker's line and outside text wrapped; outside content
// taints the session before the next model call, and only the person's own
// words make an address known. The reply answers the newest one taken.
func (a *Agent) join(ctx context.Context, t *turn, msgs, added []provider.Message, adds []additionMsg) (allMsgs, allAdded []provider.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, add := range adds {
		content, outside := userContent(add.msg)
		if outside {
			t.state.Tainted = true
		}
		user := provider.Message{Role: provider.RoleUser, Content: content}
		for u := range knownURLs([]provider.Message{user}) {
			t.known[u] = struct{}{}
		}
		msgs, added = append(msgs, user), append(added, user)
		t.kept = append(t.kept, user)
		t.taken = append(t.taken, add.id)
		t.msg.Ref = add.msg.Ref
		a.in.additions.Add(ctx, 1)
	}
	return msgs, added
}

// endRunIfWaiting ends the turn's CLI run if it can take an addition, and
// reports whether it did. The caller holds t.mu.
func (a *Agent) endRunIfWaiting(ctx context.Context, t *turn) bool {
	if len(a.waiting(ctx, t)) == 0 {
		return false
	}
	t.runEnded = true
	t.endRun(errAdditionWaiting)
	return true
}

// heldBack says why a CLI run's call must not start, nil when it may
// (R-LIVE-2): the run was ended, or a message waits, which ends the run if the
// turn can take it. It runs before the call is numbered. The caller holds t.mu.
func (a *Agent) heldBack(ctx context.Context, t *turn) error {
	var err error
	switch {
	case t.runEnded || a.endRunIfWaiting(ctx, t):
		err = errAdditionWaiting
	case len(a.behind(ctx, t)) > 0:
		err = errNoRoomForAddition
	default:
		return nil
	}
	a.in.skippedCalls.Add(ctx, 1)
	return err
}

// runCall makes the step's model call under a context the core can end at a
// tool boundary (R-LIVE-3).
func (a *Agent) runCall(ctx context.Context, t *turn, task router.TaskReq, req provider.ChatRequest) (provider.ChatResponse, turns.Outcome) {
	callCtx, end := context.WithCancelCause(ctx)
	defer end(nil)
	t.mu.Lock()
	t.endRun, t.runEnded = end, false
	t.mu.Unlock()
	return a.call(callCtx, task, req, &t.record, t.toolCalls)
}

// quote sends text at once, marked as overtaken by a later message
// (R-LIVE-5), under a key from the turn, so a rerun does not send it twice. A
// failure to store it leaves it to go out with the turn, so nothing is lost.
// The person has read it, so it stays in the history even if the turn fails.
func (a *Agent) quote(ctx context.Context, t *turn, text string) {
	m := event.Outgoing{IdemKey: t.event.IdemKey + ":earlier:" + strconv.Itoa(t.quotes), Transport: t.transport, Conv: t.replyConv, Text: text, Superseded: true}
	t.quotes++
	t.kept = append(t.kept, provider.Message{Role: provider.RoleAssistant, Content: text})
	if err := a.d.Outbox.Enqueue(ctx, m); err != nil {
		a.log.Warn("agent: earlier answer not stored; it goes with the turn", "conv", t.event.Conv, "err", err)
		t.out = append(t.out, m)
		a.observeQuote(ctx, false)
		return
	}
	a.observeQuote(ctx, true)
}
