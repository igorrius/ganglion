package telegram

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The reactions, from the Bot API's ReactionTypeEmoji list, so each is valid.
const (
	emojiQueued   = "👀"
	emojiThinking = "🤔"
	emojiTool     = "\U0001F468\u200d\U0001F4BB" // a man at a computer
	emojiAdded    = "👌"
)

const (
	// reactionTick is how often the Reactor looks at what is due.
	reactionTick = 200 * time.Millisecond
	// reactionMaxAge bounds an entry: a turn that never reported its end, such
	// as one cut by a crash, leaks nothing and leaves no reaction for good.
	reactionMaxAge = 15 * time.Minute
	// reactionCallTimeout bounds one call to the Bot API.
	reactionCallTimeout = 5 * time.Second
)

// reactionOutcome is what became of one call, for the metric.
type reactionOutcome string

const (
	reactionSet         reactionOutcome = "set"
	reactionCleared     reactionOutcome = "cleared"
	reactionRefused     reactionOutcome = "refused"
	reactionRateLimited reactionOutcome = "rate_limited"
	reactionFailed      reactionOutcome = "failed"
)

// ReactorConfig is when reactions change. After 0 turns them off.
type ReactorConfig struct {
	After  time.Duration // work takes this long before anything shows
	Dwell  time.Duration // a reaction stays this long before the next
	Linger time.Duration // a reaction stays this long after the turn ended
	Now    func() time.Time
}

// DefaultReactorConfig holds the starting values; the owner's live check on a
// phone settles them (ADR-0051).
func DefaultReactorConfig() ReactorConfig {
	return ReactorConfig{After: 8 * time.Second, Dwell: 3 * time.Second, Linger: 800 * time.Millisecond, Now: time.Now}
}

// Reactor shows work that takes long as a reaction on the person's message
// (R-LIVE-7, ADR-0051). A reaction is a hint: outside the outbox, with no
// retries, and a refusal or a 429 never delays a message. Only private chats:
// in a group everyone would see it.
type Reactor struct {
	c       *client
	cfg     ReactorConfig
	log     *slog.Logger
	outcome metric.Int64Counter

	mu     sync.Mutex // guards msgs and pause; never held during a call
	msgs   map[reactionKey]*reactionState
	paused time.Time // no call before this, after a 429
}

type reactionKey struct{ chat, msg int64 }

// reactionState is one message: what it should show and what it shows.
type reactionState struct {
	first   time.Time // when it was first reported
	wanted  string
	shown   string
	lastSet time.Time
	ended   bool
	endAt   time.Time
	refused bool // a call failed: not tried again for this message
}

// NewReactor returns a Reactor with a Bot API client of its own: the agent needs
// it to report stages, and the transport needs the agent's command list, so it
// cannot lean on the transport. baseURL "" is DefaultAPIURL.
func NewReactor(token, baseURL string, cfg ReactorConfig, log *slog.Logger) *Reactor {
	outcome, _ := otel.Meter("ganglion/telegram").Int64Counter("ganglion.telegram.reactions",
		metric.WithDescription("Calls that set or took off a reaction, by outcome."))
	return &Reactor{c: newClient(baseURL, token, nil), cfg: cfg, log: log, outcome: outcome, msgs: map[reactionKey]*reactionState{}}
}

// Report takes a stage of a person's message. It never blocks and does no I/O,
// so the agent can call it from anywhere. A stage of a group, of another
// transport or of a message with no number is ignored.
func (r *Reactor) Report(st event.Status) {
	if r.cfg.After <= 0 || st.Transport != Name {
		return
	}
	chat, _, err := ParseConv(st.Conv)
	if err != nil || chat <= 0 {
		return
	}
	msg, err := strconv.ParseInt(st.Ref, 10, 64)
	if err != nil {
		return
	}
	now, key := r.cfg.Now(), reactionKey{chat, msg}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.msgs[key]
	if !ok {
		if st.Stage == event.StageDone {
			return
		}
		e = &reactionState{first: now}
		r.msgs[key] = e
	}
	e.apply(st.Stage, now, r.cfg.Linger)
}

// apply moves the message to a stage: never back from a tool to thinking, and
// 👌 stays once it is set.
func (e *reactionState) apply(st event.Stage, now time.Time, linger time.Duration) {
	switch st {
	case event.StageQueued:
		if e.wanted == "" {
			e.wanted = emojiQueued
		}
	case event.StageThinking:
		if e.wanted != emojiTool && e.wanted != emojiAdded {
			e.wanted = emojiThinking
		}
	case event.StageTool:
		if e.wanted != emojiAdded {
			e.wanted = emojiTool
		}
	case event.StageAdded:
		e.wanted = emojiAdded
	case event.StageDone:
		e.finish(now.Add(linger))
	}
}

// finish makes the message's reaction come off at the time given.
func (e *reactionState) finish(at time.Time) {
	if !e.ended {
		e.ended, e.endAt = true, at
	}
}

// Noting wraps the sink that stores a message, behind the access list: only a
// message that was let in reports anything, so a stranger learns nothing.
func (r *Reactor) Noting(next transport.Sink) transport.Sink {
	return func(ctx context.Context, e event.Event) error {
		if err := next(ctx, e); err != nil {
			return err
		}
		if e.Kind == event.KindMessage {
			var m event.Message
			if json.Unmarshal(e.Payload, &m) == nil {
				r.Report(event.Status{Transport: e.Source, Conv: e.Conv, Ref: m.Ref, Stage: event.StageQueued})
			}
		}
		return nil
	}
}

// Run looks at what is due until ctx ends.
func (r *Reactor) Run(ctx context.Context) error {
	if r.cfg.After <= 0 {
		<-ctx.Done()
		return nil
	}
	tick := time.NewTicker(reactionTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			r.step(ctx, r.cfg.Now())
		}
	}
}

// reactionCall is one reaction to set; "" takes it off.
type reactionCall struct {
	key   reactionKey
	emoji string
}

// step makes the calls due at now, one at a time. It stops at a 429.
func (r *Reactor) step(ctx context.Context, now time.Time) {
	for _, call := range r.due(now) {
		cctx, cancel := context.WithTimeout(ctx, reactionCallTimeout)
		err := r.c.setMessageReaction(cctx, call.key.chat, call.key.msg, call.emoji)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if r.settle(ctx, call, now, err) {
			return
		}
	}
}

// due lists the calls to make at now, in a fixed order, and forgets the entries
// that need no call.
func (r *Reactor) due(now time.Time) []reactionCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.paused) {
		return nil
	}
	var out []reactionCall
	for key, e := range r.msgs {
		if now.Sub(e.first) > reactionMaxAge {
			e.finish(now)
		}
		switch {
		case e.ended && now.Before(e.endAt):
		case e.ended && e.shown == "":
			delete(r.msgs, key)
		case e.ended:
			out = append(out, reactionCall{key, ""})
		case r.showsNow(e, now):
			out = append(out, reactionCall{key, e.wanted})
		}
	}
	slices.SortFunc(out, func(a, b reactionCall) int {
		return cmp.Or(cmp.Compare(a.key.chat, b.key.chat), cmp.Compare(a.key.msg, b.key.msg))
	})
	return out
}

// showsNow reports whether the wanted reaction goes up at now: 👌 at once, any
// other past the threshold and the dwell, so it does not flicker.
func (r *Reactor) showsNow(e *reactionState, now time.Time) bool {
	switch {
	case e.refused || e.wanted == e.shown:
		return false
	case e.wanted == emojiAdded:
		return true
	case now.Sub(e.first) < r.cfg.After:
		return false
	}
	return e.shown == "" || now.Sub(e.lastSet) >= r.cfg.Dwell
}

// settle records how a call went; true means a 429, so reactions pause. A failed
// call is not tried again for that message: nothing retries in a loop.
func (r *Reactor) settle(ctx context.Context, call reactionCall, now time.Time, err error) (paused bool) {
	outcome := reactionFailed
	ae, isAPI := errors.AsType[*apiError](err)
	switch {
	case err == nil && call.emoji == "":
		outcome = reactionCleared
	case err == nil:
		outcome = reactionSet
	case isAPI && ae.Code == http.StatusTooManyRequests:
		outcome, paused = reactionRateLimited, true
	case isAPI:
		outcome = reactionRefused
	}
	r.outcome.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(outcome))))
	if err != nil {
		r.log.Debug("telegram: reaction not set", "chat", call.key.chat, "outcome", outcome, "err", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.msgs[call.key]
	switch {
	case paused:
		r.paused = now.Add(max(ae.RetryAfter, time.Second))
	case !ok:
	case call.emoji == "":
		delete(r.msgs, call.key)
	case err != nil:
		e.refused = true
	default:
		e.shown, e.lastSet = call.emoji, now
	}
	return paused
}
