package node

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const hookToken = "7001:test-secret"

// box collects what a Webhook stores: incoming events and alerts.
type box struct {
	mu     sync.Mutex
	events []event.Event
	alerts []event.Outgoing
}

func (b *box) sink(_ context.Context, e event.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return nil
}

func (b *box) enqueue(_ context.Context, msgs ...event.Outgoing) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.alerts = append(b.alerts, msgs...)
	return nil
}

func (b *box) counts() (events, alerts int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events), len(b.alerts)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// R-DLV-19: a public address that does not reach the node must not cut
// the owner off. The node registers no webhook, tells the admins why, and
// polls, so the owner's next message still arrives.
func TestUnreachableWebhookAlertsAndPolls(t *testing.T) {
	fake := telegramtest.NewServer(t, hookToken)
	unreachable := func(context.Context, string) error { return errors.New("another process answered") }
	tr, err := telegram.New(hookToken, telegram.WithBaseURL(fake.URL()), telegram.WithPollTimeout(time.Second),
		telegram.WithWebhook("https://wrong.example", unreachable))
	if err != nil {
		t.Fatal(err)
	}
	b := &box{}
	w := Webhook{Poller{
		Transport: tr, Sink: b.sink, Enqueue: b.enqueue, Started: time.UnixMilli(1), Log: slog.New(slog.DiscardHandler),
		Admins: alert.Admins{
			List:    func(context.Context) ([]string, error) { return []string{"telegram:user:42"}, nil },
			Private: telegram.PrivateConv,
		},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "/restart"})
	waitFor(t, "the alert and the polled message", func() bool { e, a := b.counts(); return e == 1 && a == 1 })
	if text := b.alerts[0].Text; !strings.Contains(text, "not registered") || !strings.Contains(text, "http.public_url") {
		t.Errorf("alert %q", text)
	}
	if n := fake.Calls("setWebhook"); n != 0 {
		t.Errorf("setWebhook called %d times for an address that does not reach the node", n)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
}
