package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport"
)

// WebhookPath is the fixed path of the webhook on the node's public
// listener. Nothing secret is in it: proxies log paths (ADR-0078).
const WebhookPath = "/telegram/webhook"

// secretHeader carries the secret Telegram was given at setWebhook.
const secretHeader = "X-Telegram-Bot-Api-Secret-Token"

// hookEvery is how often the webhook's state is read from Telegram, for
// /status and for the guard against a lock-out.
const hookEvery = time.Minute

// failingReadings is how many readings in a row with updates waiting and
// deliveries failing return the node to polling for the run (R-DLV-19).
const failingReadings = 3

// The ways Serve ends without the webhook. After ErrWebhookUnreachable and
// ErrWebhookFailing the caller polls with Run for the rest of the run;
// after ErrWebhookTaken nobody receives, as after ErrConflict (R-DLV-19).
var (
	// ErrWebhookUnreachable: the node's public address did not reach this
	// process, so no webhook was registered.
	ErrWebhookUnreachable = errors.New("telegram: the public address does not reach this node; the webhook is not registered")
	// ErrWebhookFailing: updates wait at Telegram and its deliveries to the
	// webhook keep failing.
	ErrWebhookFailing = errors.New("telegram: Telegram's deliveries to the webhook keep failing")
	// ErrWebhookTaken: the bot's webhook is no longer this node's.
	ErrWebhookTaken = errors.New("telegram: the bot's webhook is not this node's any more")
)

// ReachFunc reports whether publicURL reaches this process; nil when it
// does (publichttp.Listener.Reaches).
type ReachFunc func(ctx context.Context, publicURL string) error

// WebhookRecord remembers that this node set a webhook, so a node set back
// to polling removes its own webhook and never anybody else's (R-DLV-18).
type WebhookRecord interface {
	// URL is the webhook this node set; "" when it set none.
	URL(ctx context.Context) (string, error)
	Set(ctx context.Context, url string) error
	Clear(ctx context.Context) error
}

// noRecord remembers nothing: a node without a database.
type noRecord struct{}

func (noRecord) URL(context.Context) (string, error) { return "", nil }
func (noRecord) Set(context.Context, string) error   { return nil }
func (noRecord) Clear(context.Context) error         { return nil }

// neverReaches is the check of a transport that was given no public
// address.
func neverReaches(context.Context, string) error {
	return errors.New("no public address is set")
}

// WithWebhook sets the public address Telegram calls and the check that it
// reaches this process; Serve needs both.
func WithWebhook(publicURL string, reaches ReachFunc) Option {
	return func(c *config) { c.publicURL, c.reaches = publicURL, reaches }
}

// WithWebhookRecord sets where the node remembers the webhook it set; the
// default remembers nothing.
func WithWebhookRecord(r WebhookRecord) Option { return func(c *config) { c.record = r } }

// webhookSecret is the secret Telegram repeats in every webhook request:
// derived from the bot token, so there is nothing to configure or store,
// it survives a restart and changes with the token. The token cannot be
// computed back from it (ADR-0078).
func webhookSecret(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("ganglion telegram webhook v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

// hook is the webhook mode's part of a Transport.
type hook struct {
	publicURL string
	reaches   ReachFunc
	record    WebhookRecord
	secret    []byte
	every     time.Duration
	count     hookCounter

	order sync.Mutex // one update at a time, as polling hands them

	mu   sync.Mutex
	on   bool // Serve is past the drain: requests are taken
	me   user
	sink transport.Sink
}

// url is the address given to setWebhook.
func (h *hook) url() string { return strings.TrimRight(h.publicURL, "/") + WebhookPath }

// open lets requests in, for the bot me and into sink.
func (h *hook) open(me user, sink transport.Sink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.on, h.me, h.sink = true, me, sink
}

func (h *hook) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.on, h.sink = false, nil
}

func (h *hook) receiving() (user, transport.Sink, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.me, h.sink, h.on
}

// Serve receives updates by a webhook (R-DLV-18). It checks that the
// public address reaches this process, takes the updates that waited at
// Telegram by polling, registers the webhook and then watches it once a
// minute; the requests themselves come through WebhookHandler. An update
// gets 200 only once it is in the inbox (R-DLV-2).
//
// Serve returns ErrWebhookUnreachable or ErrWebhookFailing when the caller
// should poll with Run for this run, ErrWebhookTaken or ErrConflict when
// somebody else holds the bot, the error on 401, and ctx.Err() when ctx
// ends.
func (t *Transport) Serve(ctx context.Context, sink transport.Sink) error {
	b := backoff.New(t.retry)
	me, err := t.whoAmI(ctx, b)
	if err != nil {
		return err
	}
	defer t.hints(ctx)()
	if err := t.hook.reaches(ctx, t.hook.publicURL); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		t.log.Error("telegram: the public address does not reach this node; polling for this run", "reason", err)
		t.health.fellBack(texts.StatusReasonWebhookFallback)
		return fmt.Errorf("%w: %w", ErrWebhookUnreachable, err)
	}
	before, err := t.drain(ctx, sink, me, b)
	if err != nil {
		return err
	}
	t.hook.open(me, sink)
	defer t.hook.close()
	if err := t.register(ctx, b); err != nil {
		return err
	}
	t.log.Info("telegram: webhook registered", "bot_id", me.ID, "username", me.Username)
	return t.watch(ctx, before, b)
}

// drain takes the updates that waited at Telegram while the node was away
// by polling, and returns the time of the last delivery error Telegram
// had before this start. After a stop Telegram's next try can be two
// minutes off, where a poll fetches at once (the spike of task 6.6).
func (t *Transport) drain(ctx context.Context, sink transport.Sink, me user, b *backoff.Backoff) (int64, error) {
	var info webhookInfo
	if err := t.retrying(ctx, b, func() (err error) { info, err = t.c.getWebhookInfo(ctx); return err }); err != nil {
		return 0, err
	}
	if info.PendingUpdateCount == 0 {
		return info.LastErrorDate, nil
	}
	t.log.Info("telegram: updates waited at Telegram; taking them by polling first", "pending", info.PendingUpdateCount)
	if err := t.retrying(ctx, b, func() error { return t.c.deleteWebhook(ctx) }); err != nil {
		return 0, err
	}
	sinkRetry := backoff.New(t.retry)
	var offset int64
	for {
		var ups []update
		if err := t.retrying(ctx, b, func() (err error) { ups, err = t.c.getUpdates(ctx, offset, 0); return err }); err != nil {
			return 0, err
		}
		if len(ups) == 0 {
			return info.LastErrorDate, nil // the empty answer confirmed the last one
		}
		var err error
		if offset, err = t.hand(ctx, sink, me, ups, offset, sinkRetry); err != nil {
			return 0, err
		}
	}
}

// register records that this node sets the webhook, then sets it. The
// record comes first: a node stopped between the two must still know the
// webhook is its own, or set back to polling it would leave it there and
// stop on 409. It is done at every start: setWebhook is idempotent.
func (t *Transport) register(ctx context.Context, b *backoff.Backoff) error {
	url := t.hook.url()
	if err := t.hook.record.Set(ctx, url); err != nil {
		return fmt.Errorf("telegram: record the webhook: %w", err)
	}
	if err := t.retrying(ctx, b, func() error { return t.c.setWebhook(ctx, url, string(t.hook.secret)) }); err != nil {
		return err
	}
	t.health.hookOn()
	return nil
}

// watch reads the webhook's state until ctx ends or a guard trips:
// somebody else's webhook or none is ErrWebhookTaken, and failingReadings
// readings in a row with updates waiting and a recent delivery error are
// ErrWebhookFailing. A delivery error is recent when it is newer than
// before, the one Telegram had at this start, and no older than
// failingReadings readings (and a second: Telegram gives whole seconds).
func (t *Transport) watch(ctx context.Context, before int64, b *backoff.Backoff) error {
	bad := 0
	for {
		if err := sleep(ctx, t.hook.every); err != nil {
			return err
		}
		var info webhookInfo
		if err := t.retrying(ctx, b, func() (err error) { info, err = t.c.getWebhookInfo(ctx); return err }); err != nil {
			return err
		}
		if info.URL != t.hook.url() {
			t.log.Error("telegram: the bot's webhook is not this node's; receiving stopped")
			t.health.stoppedNow(texts.StatusReasonWebhookTaken)
			return ErrWebhookTaken
		}
		recent := info.LastErrorDate > before && t.now().Sub(time.Unix(info.LastErrorDate, 0)) <= failingReadings*t.hook.every+time.Second
		failing := info.PendingUpdateCount > 0 && recent
		t.health.hookRead(info.PendingUpdateCount, failing)
		if !failing {
			bad = 0
			continue
		}
		if bad++; bad >= failingReadings {
			t.log.Error("telegram: deliveries to the webhook keep failing; polling for this run", "pending", info.PendingUpdateCount)
			t.health.fellBack(texts.StatusReasonWebhookFailing)
			return ErrWebhookFailing
		}
	}
}

// retrying calls the Bot API through call until it answers or onError
// stops.
func (t *Transport) retrying(ctx context.Context, b *backoff.Backoff, call func() error) error {
	for {
		err := call()
		if err == nil {
			b.Reset()
			return nil
		}
		if stop := t.onError(ctx, err, b); stop != nil {
			return stop
		}
	}
}

// dropOwnWebhook removes the webhook this node recorded setting, before it
// polls: the file was set back to polling, or the webhook is off for this
// run. A webhook the node did not set stays, and getUpdates answers 409
// (R-DLV-8).
func (t *Transport) dropOwnWebhook(ctx context.Context, b *backoff.Backoff) error {
	url, err := t.hook.record.URL(ctx)
	if err != nil {
		return fmt.Errorf("telegram: read the webhook record: %w", err)
	}
	if url == "" {
		return nil
	}
	if err := t.retrying(ctx, b, func() error { return t.c.deleteWebhook(ctx) }); err != nil {
		return err
	}
	if err := t.hook.record.Clear(ctx); err != nil {
		return fmt.Errorf("telegram: clear the webhook record: %w", err)
	}
	t.log.Info("telegram: removed the webhook this node set; polling")
	return nil
}

// WebhookHandler is the route of the webhook on the public listener. It
// answers 503 until Serve takes requests, so Telegram tries again.
func (t *Transport) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := t.receive(r)
		t.hook.count.add(r.Context(), o)
		w.WriteHeader(hookStatus[o])
	})
}

// receive handles one webhook request. The secret is checked before the
// body is read; a request that fails it goes no further and its body is
// never logged.
func (t *Transport) receive(r *http.Request) hookOutcome {
	if r.Method != http.MethodPost {
		return hookBadMethod
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(secretHeader)), t.hook.secret) != 1 {
		// Debug: anybody can send these, and the counter already has them.
		t.log.Debug("telegram: webhook request without the secret refused")
		return hookForbidden
	}
	me, sink, on := t.hook.receiving()
	if !on {
		return hookNotReady
	}
	body, err := io.ReadAll(r.Body) // capped by the listener
	if err != nil {
		t.log.Warn("telegram: webhook body not read", "err", err)
		return hookTooLarge
	}
	var u update
	if err := json.Unmarshal(body, &u); err != nil || u.UpdateID == 0 {
		// 200: Telegram would only send the same bytes again.
		t.log.Warn("telegram: webhook body is not an update", "bytes", len(body))
		return hookUnparsed
	}
	t.hook.order.Lock()
	defer t.hook.order.Unlock()
	return t.take(r.Context(), sink, me, u)
}

// take gives one update to sink the way hand gives a polled one: the same
// event and the same idemKey, so a repeat is rejected by the inbox
// (R-DLV-1).
func (t *Transport) take(ctx context.Context, sink transport.Sink, me user, u update) hookOutcome {
	t.seen(u)
	t.noteTopic(ctx, u)
	t.health.receivedNow()
	if t.intercept(ctx, u) {
		return hookIntake
	}
	e, ok := t.toEvent(me, u)
	if !ok {
		t.answerCallback(ctx, u)
		return hookSkipped
	}
	err := sink(ctx, e)
	switch {
	case errors.Is(err, transport.ErrDropped):
		t.answerCallback(ctx, u) // no handler will
		return hookDropped
	case err != nil:
		t.log.Warn("telegram: sink failed, update not confirmed", "update_id", u.UpdateID, "conv", e.Conv, "err", err)
		return hookSinkFailed
	}
	return hookStored
}
