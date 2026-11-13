package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// memRecord is a WebhookRecord in memory.
type memRecord struct {
	mu  sync.Mutex
	url string
}

func (r *memRecord) URL(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.url, nil
}

func (r *memRecord) Set(_ context.Context, url string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.url = url
	return nil
}

func (r *memRecord) Clear(ctx context.Context) error { return r.Set(ctx, "") }

func reachable(context.Context, string) error { return nil }

// hooked is a transport in webhook mode: front is its public address, a
// server that passes the webhook's path to the transport, or to refuse
// when that is set.
type hooked struct {
	tr     *Transport
	fake   *telegramtest.Server
	front  *httptest.Server
	record *memRecord
	health *Health

	mu     sync.Mutex
	refuse int // the status front answers instead of the transport; 0 passes on
}

func (h *hooked) refusing(code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuse = code
}

func newHooked(t *testing.T, reaches ReachFunc) *hooked {
	t.Helper()
	h := &hooked{fake: telegramtest.NewServer(t, testToken), record: &memRecord{}, health: NewHealth(time.Now)}
	h.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		refuse := h.refuse
		h.mu.Unlock()
		switch {
		case r.URL.Path != WebhookPath:
			http.NotFound(w, r)
		case refuse != 0:
			w.WriteHeader(refuse)
		default:
			h.tr.WebhookHandler().ServeHTTP(w, r)
		}
	}))
	t.Cleanup(h.front.Close)
	tr, err := New(testToken, WithBaseURL(h.fake.URL()), WithPollTimeout(time.Second), WithHealth(h.health),
		WithWebhook(h.front.URL+"/", reaches), WithWebhookRecord(h.record))
	if err != nil {
		t.Fatal(err)
	}
	tr.retry.Min, tr.hook.every = 10*time.Millisecond, time.Hour // watching sets its own pace
	h.tr = tr
	return h
}

// watching is a transport in webhook mode that reads the webhook's state
// every few milliseconds, for the tests of the guards.
func watching(t *testing.T) *hooked {
	t.Helper()
	h := newHooked(t, reachable)
	h.tr.hook.every = 10 * time.Millisecond
	return h
}

// serve starts tr.Serve and returns a function that waits for its result.
func serve(t *testing.T, tr *Transport, sink transport.Sink) (cancel func(), result func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Serve(ctx, sink) }()
	t.Cleanup(cancel)
	return cancel, func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return")
			return nil
		}
	}
}

func (h *hooked) registered(t *testing.T) {
	t.Helper()
	eventually(t, "the webhook registered", func() bool { return len(h.fake.WebhookSets()) > 0 })
}

// post sends body to the webhook's address as a stranger would.
func (h *hooked) post(t *testing.T, method, secret, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, h.front.URL+WebhookPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set(secretHeader, secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// R-DLV-18: the webhook is registered as the node's file says, an update
// goes the way a polled one goes, and nobody polls.
func TestWebhookPutsMessagesInOrder(t *testing.T) {
	h := newHooked(t, reachable)
	c := &collector{}
	cancel, result := serve(t, h.tr, c.sink)
	h.registered(t)
	var ids []int64
	for _, text := range []string{"one", "two", "three"} {
		ids = append(ids, h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: text}))
	}
	eventually(t, "three events", func() bool { return len(c.got()) == 3 })
	for i, want := range []string{"one", "two", "three"} {
		e := c.got()[i]
		if m := payload(t, e); m.Text != want || m.From != "telegram:user:42" {
			t.Errorf("event %d payload %+v", i, m)
		}
		if e.IdemKey != idemKey(7001, ids[i]) || e.Conv != "telegram:42" || e.Source != Name {
			t.Errorf("event %d = %+v", i, e)
		}
	}
	set := h.fake.WebhookSets()[0]
	if set.URL != h.front.URL+WebhookPath || set.Secret != webhookSecret(testToken) || set.MaxConnections != 1 ||
		set.DropPending || !slices.Equal(set.AllowedUpdates, allowedUpdates) {
		t.Errorf("setWebhook %+v", set)
	}
	if strings.Contains(set.URL, set.Secret) || strings.Contains(set.Secret, "secret") {
		t.Errorf("the secret is in the address, or the token is in the secret: %+v", set)
	}
	if url, _ := h.record.URL(t.Context()); url != set.URL {
		t.Errorf("recorded %q, want %q", url, set.URL)
	}
	if n := h.fake.Calls("getUpdates"); n != 0 {
		t.Errorf("getUpdates called %d times in webhook mode", n)
	}
	eventually(t, "all confirmed", func() bool { return h.fake.WebhookInfo().PendingUpdateCount == 0 })
	cancel()
	if err := result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve after cancel = %v, want context.Canceled", err)
	}
	if n := h.fake.Calls("deleteWebhook"); n != 0 {
		t.Errorf("deleteWebhook called %d times at a stop", n)
	}
}

// R-DLV-2 in webhook mode: 200 only once the update is in the inbox; a
// failed write is sent again and arrives once.
func TestWebhookConfirmsOnlyAfterSink(t *testing.T) {
	h := newHooked(t, reachable)
	c := &collector{failOn: func(_ event.Event, n int) bool { return n < 2 }}
	serve(t, h.tr, c.sink)
	h.registered(t)
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "kept"})
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "after"})
	eventually(t, "both events", func() bool { return len(c.got()) == 2 })
	if a, b := payload(t, c.got()[0]).Text, payload(t, c.got()[1]).Text; a != "kept" || b != "after" {
		t.Errorf("order %q, %q", a, b)
	}
	if info := h.fake.WebhookInfo(); !strings.Contains(info.LastErrorMessage, "500") {
		t.Errorf("Telegram saw %q, want a 500", info.LastErrorMessage)
	}
}

// Abuse: a request that does not carry the secret never reaches the sink,
// whatever it says (ADR-0078).
func TestWebhookRefusesForgedRequests(t *testing.T) {
	rec := telemetrytest.Install(t)
	h := newHooked(t, reachable)
	c := &collector{}
	forged := `{"update_id":1,"message":{"message_id":1,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"first_name":"Admin"},"text":"/restart"}}`
	secret := webhookSecret(testToken)
	if code := h.post(t, http.MethodPost, secret, forged); code != http.StatusServiceUnavailable {
		t.Errorf("before Serve takes requests: %d, want 503", code)
	}
	serve(t, h.tr, c.sink)
	h.registered(t)
	for name, tc := range map[string]struct {
		method, secret, body string
		want                 int
	}{
		"no secret":           {http.MethodPost, "", forged, http.StatusForbidden},
		"a wrong secret":      {http.MethodPost, secret[:len(secret)-1] + "0", forged, http.StatusForbidden},
		"the token":           {http.MethodPost, testToken, forged, http.StatusForbidden},
		"a prefix":            {http.MethodPost, secret[:10], forged, http.StatusForbidden},
		"a GET":               {http.MethodGet, secret, "", http.StatusMethodNotAllowed},
		"a GET, no secret":    {http.MethodGet, "", "", http.StatusMethodNotAllowed},
		"not JSON":            {http.MethodPost, secret, "<html>", http.StatusOK},
		"JSON, not an update": {http.MethodPost, secret, `{"message":{"text":"hi"}}`, http.StatusOK},
	} {
		if code := h.post(t, tc.method, tc.secret, tc.body); code != tc.want {
			t.Errorf("%s: %d, want %d", name, code, tc.want)
		}
	}
	if got := c.got(); len(got) != 0 {
		t.Fatalf("a forged request reached the sink: %+v", got)
	}
	if n := rec.Value(t, "ganglion.telegram.webhook.requests", map[string]string{"outcome": "forbidden"}); n != 4 {
		t.Errorf("forbidden counted %v, want 4", n)
	}
	// The same body with the secret is an update.
	if code := h.post(t, http.MethodPost, secret, forged); code != http.StatusOK || len(c.got()) != 1 {
		t.Errorf("with the secret: %d, %d events", code, len(c.got()))
	}
}

// Updates that waited while the node was away are taken by polling before
// the webhook is registered: Telegram's own next try can be minutes off.
func TestWebhookTakesWaitingUpdatesByPollingFirst(t *testing.T) {
	h := newHooked(t, reachable)
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "waited one"})
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "waited two"})
	c := &collector{}
	serve(t, h.tr, c.sink)
	h.registered(t)
	if got := c.got(); len(got) != 2 || payload(t, got[0]).Text != "waited one" || payload(t, got[1]).Text != "waited two" {
		t.Fatalf("events before the webhook: %+v", got)
	}
	if m := h.fake.Methods(); !slices.Equal(m[:2], []string{"getWebhookInfo", "deleteWebhook"}) || !slices.Contains(m, "setWebhook") {
		t.Errorf("methods %v", m)
	}
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "live"})
	eventually(t, "the live one", func() bool { return len(c.got()) == 3 })
	polls := h.fake.Calls("getUpdates")
	if polls == 0 {
		t.Error("nothing was polled")
	}
	time.Sleep(50 * time.Millisecond)
	if n := h.fake.Calls("getUpdates"); n != polls {
		t.Errorf("polling went on after the webhook: %d calls, then %d", polls, n)
	}
}

// R-DLV-19, the first guard: a public address that does not reach the node
// registers nothing, and the node polls for the run.
func TestWebhookNotRegisteredWhenUnreachable(t *testing.T) {
	h := newHooked(t, func(context.Context, string) error { return errors.New("another process answered") })
	c := &collector{}
	_, result := serve(t, h.tr, c.sink)
	if err := result(); !errors.Is(err, ErrWebhookUnreachable) {
		t.Fatalf("Serve = %v, want ErrWebhookUnreachable", err)
	}
	if n := h.fake.Calls("setWebhook"); n != 0 {
		t.Fatalf("setWebhook called %d times", n)
	}
	run(t, h.tr, c.sink)
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "still here"})
	eventually(t, "the polled event", func() bool { return len(c.got()) == 1 })
	l := line(t, h.health, time.Now())
	if l.State != status.Warn || l.Reason != texts.StatusReasonWebhookFallback || l.Line != texts.StatusTelegram {
		t.Errorf("status %+v", l)
	}
}

// R-DLV-19, the second guard: updates wait and deliveries keep failing, so
// the node removes its webhook and polls; nothing is lost.
func TestWebhookFallsBackWhenDeliveriesFail(t *testing.T) {
	h := watching(t)
	c := &collector{}
	_, result := serve(t, h.tr, c.sink)
	h.registered(t)
	h.refusing(http.StatusNotFound) // the domain was removed
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "/restart"})
	if err := result(); !errors.Is(err, ErrWebhookFailing) {
		t.Fatalf("Serve = %v, want ErrWebhookFailing", err)
	}
	run(t, h.tr, c.sink)
	eventually(t, "the update by polling", func() bool { return len(c.got()) == 1 })
	if payload(t, c.got()[0]).Text != "/restart" {
		t.Errorf("event %+v", c.got()[0])
	}
	if n := h.fake.Calls("deleteWebhook"); n != 1 {
		t.Errorf("deleteWebhook called %d times, want 1", n)
	}
	if url, _ := h.record.URL(t.Context()); url != "" {
		t.Errorf("the record still says %q", url)
	}
	if l := line(t, h.health, time.Now()); l.State != status.Warn || l.Reason != texts.StatusReasonWebhookFailing {
		t.Errorf("status %+v", l)
	}
}

// A quiet webhook is no failure: nothing waits, so old errors do not count.
func TestQuietWebhookStays(t *testing.T) {
	h := watching(t)
	h.tr.hook.every = 50 * time.Millisecond // three readings outlast the one refused delivery
	h.fake.SetWebhookRetry(time.Millisecond)
	c := &collector{}
	_, result := serve(t, h.tr, c.sink)
	h.registered(t)
	h.refusing(http.StatusInternalServerError)
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "late"})
	eventually(t, "a failed delivery", func() bool { return h.fake.WebhookInfo().LastErrorDate != 0 })
	h.refusing(0)
	eventually(t, "the event", func() bool { return len(c.got()) == 1 })
	time.Sleep(250 * time.Millisecond) // five readings
	select {
	case err := <-func() chan error { ch := make(chan error, 1); go func() { ch <- result() }(); return ch }():
		t.Fatalf("Serve ended: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if l := line(t, h.health, time.Now()); l.State != status.OK || l.Line != texts.StatusTelegramWebhook {
		t.Errorf("status %+v", l)
	}
}

// Somebody else takes the bot: the node says so and does not fight for it.
func TestWebhookTakenIsNotSetAgain(t *testing.T) {
	h := watching(t)
	_, result := serve(t, h.tr, (&collector{}).sink)
	h.registered(t)
	h.fake.SetWebhook(true)
	if err := result(); !errors.Is(err, ErrWebhookTaken) {
		t.Fatalf("Serve = %v, want ErrWebhookTaken", err)
	}
	if n := h.fake.Calls("setWebhook"); n != 1 {
		t.Errorf("setWebhook called %d times, want 1", n)
	}
	if l := line(t, h.health, time.Now()); l.State != status.Fail || l.Reason != texts.StatusReasonWebhookTaken {
		t.Errorf("status %+v", l)
	}
}

// Set back to polling, the node removes the webhook it set and polls.
func TestPollingRemovesTheWebhookItSet(t *testing.T) {
	h := newHooked(t, reachable)
	c := &collector{}
	cancel, result := serve(t, h.tr, c.sink)
	h.registered(t)
	cancel()
	_ = result()
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "while away"})
	run(t, h.tr, c.sink)
	eventually(t, "the event", func() bool { return len(c.got()) == 1 })
	if url, _ := h.record.URL(t.Context()); url != "" || h.fake.WebhookInfo().URL != "" {
		t.Errorf("record %q, Telegram %q", url, h.fake.WebhookInfo().URL)
	}
}

// Abuse of the rule: a webhook the node did not set is never removed.
func TestPollingLeavesAForeignWebhook(t *testing.T) {
	h := newHooked(t, reachable)
	h.fake.SetWebhook(true)
	_, result := run(t, h.tr, (&collector{}).sink)
	if err := result(); !errors.Is(err, ErrConflict) {
		t.Fatalf("Run = %v, want ErrConflict", err)
	}
	if n := h.fake.Calls("deleteWebhook"); n != 0 || h.fake.WebhookInfo().URL != telegramtest.ForeignWebhook {
		t.Errorf("deleteWebhook called %d times; webhook now %q", n, h.fake.WebhookInfo().URL)
	}
}

// R-DLV-18: an update becomes the same event in both modes.
func TestBothModesMakeTheSameEvents(t *testing.T) {
	feed := func(fake *telegramtest.Server) {
		fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: "plain", Language: "uk"})
		fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42})                                   // no text: skipped
		fake.AddMessage(telegramtest.Incoming{ChatID: -100, UserID: 42, Title: "Family", Text: "aside"}) // not addressed
		fake.AddMessage(telegramtest.Incoming{ChatID: -100, UserID: 42, Title: "Family", Text: "you, hello", Extra: fake.AddressBot()})
		fake.AddMessage(telegramtest.Incoming{ChatID: -100, ThreadID: 7, TopicTitle: "Bills", UserID: 43, Title: "Family", Text: "x in a topic", Extra: fake.AddressBot()})
		fake.AddCallback(telegramtest.Callback{ChatID: 42, UserID: 42, MessageID: 5, Data: "not ours"})
		fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Caption: "a photo", Extra: map[string]any{
			"photo": []map[string]any{{"file_id": "f", "file_unique_id": "u", "width": 1, "height": 1}}}})
		fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "last"})
	}
	strip := func(es []event.Event) []event.Event {
		for i := range es {
			es[i].ID, es[i].At = "", time.Time{}
		}
		return es
	}
	polled := &collector{}
	fake := telegramtest.NewServer(t, testToken)
	feed(fake)
	run(t, newTransport(t, fake), polled.sink)
	eventually(t, "the polled events", func() bool { n := len(polled.got()); return n > 0 && payload(t, polled.got()[n-1]).Text == "last" })

	h := newHooked(t, reachable)
	pushed := &collector{}
	serve(t, h.tr, pushed.sink)
	h.registered(t)
	feed(h.fake)
	eventually(t, "the pushed events", func() bool { n := len(pushed.got()); return n > 0 && payload(t, pushed.got()[n-1]).Text == "last" })

	a, b := strip(polled.got()), strip(pushed.got())
	if len(a) != len(b) || len(a) != 5 {
		t.Fatalf("%d polled events, %d pushed", len(a), len(b))
	}
	for i := range a {
		if a[i].IdemKey != b[i].IdemKey || a[i].Conv != b[i].Conv || a[i].Kind != b[i].Kind || string(a[i].Payload) != string(b[i].Payload) {
			t.Errorf("event %d differs:\n polled %+v %s\n pushed %+v %s", i, a[i], a[i].Payload, b[i], b[i].Payload)
		}
	}
	if x, y := len(fake.Answered()), len(h.fake.Answered()); x != y || x != 1 {
		t.Errorf("presses answered: %d polled, %d pushed, want 1 each", x, y)
	}
}

// A node stopped right after setWebhook must still know the webhook is its
// own: the record is written before Telegram is told.
func TestWebhookIsRecordedBeforeItIsSet(t *testing.T) {
	h := newHooked(t, reachable)
	release := h.fake.Hold("setWebhook")
	cancel, result := serve(t, h.tr, (&collector{}).sink)
	eventually(t, "the record", func() bool { url, _ := h.record.URL(t.Context()); return url != "" })
	if n := len(h.fake.WebhookSets()); n != 0 {
		t.Fatalf("the webhook was set %d times before the record", n)
	}
	cancel()
	release()
	_ = result()
}
