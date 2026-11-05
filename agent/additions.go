package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
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

// waiting returns the unbroken run of events behind the turn that may join
// it (R-LIVE-1): it stops at the first that does not qualify, so a later
// message never overtakes an earlier one. It changes nothing, and taking is
// join's. A read that fails is logged and means nothing waits: the messages
// keep their own turns. It reads only what the turn's loop writes between
// model calls, so a CLI provider's goroutines may call it.
func (a *Agent) waiting(ctx context.Context, t *turn) []additionMsg {
	if !a.mayTake(t) {
		return nil
	}
	// Taken events stay pending until Complete, so they are read again.
	behind, err := a.d.Inbox.Behind(ctx, t.event, len(t.taken)+maxAdditionsAtOnce)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Warn("agent: inbox not read", "conv", t.event.Conv, "err", err)
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

// mayTake reports whether the turn can take an addition at all: a person's
// turn, with steps and time left (R-LIVE-1, R-LIVE-6).
func (a *Agent) mayTake(t *turn) bool {
	return t.event.Kind == event.KindMessage && t.grant == nil && !t.noTools &&
		t.record.Steps < a.cfg.MaxSteps && time.Until(t.deadline) >= minAdditionTime
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
