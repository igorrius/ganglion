package intake_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
)

const admin = "telegram:user:1"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// rig is a registry with fakes around it and records of what it did.
type rig struct {
	t      *testing.T
	reg    *intake.Registry
	clock  *clock
	logs   *bytes.Buffer
	mu     sync.Mutex
	sent   []event.Outgoing
	closed []intake.Closed
	admins map[string]bool
	after  chan time.Time
}

func newRig(t *testing.T, opts ...intake.Option) *rig {
	t.Helper()
	rg := &rig{t: t, clock: &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}, logs: &bytes.Buffer{},
		admins: map[string]bool{admin: true}, after: make(chan time.Time)}
	isAdmin := func(_ context.Context, p string) (bool, error) { return rg.admins[p], nil }
	enqueue := func(_ context.Context, m ...event.Outgoing) error {
		rg.mu.Lock()
		rg.sent = append(rg.sent, m...)
		rg.mu.Unlock()
		return nil
	}
	lang := func(context.Context, string) (texts.Lang, error) { return texts.English, nil }
	private := func(p string) (string, string, bool) {
		return "telegram", "telegram:" + p[len("telegram:user:"):], true
	}
	all := append([]intake.Option{
		intake.WithClock(rg.clock.now),
		intake.WithAfter(func(time.Duration) <-chan time.Time { return rg.after }),
		intake.WithLogger(slog.New(slog.NewTextHandler(rg.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		intake.WithClosed(func(_ context.Context, c intake.Closed) {
			rg.mu.Lock()
			rg.closed = append(rg.closed, c)
			rg.mu.Unlock()
		}),
	}, opts...)
	rg.reg = intake.New(isAdmin, enqueue, lang, private, texts.Shipped(), all...)
	return rg
}

func (rg *rig) replies() []string {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	var out []string
	for _, m := range rg.sent {
		out = append(out, m.Text)
	}
	return out
}

func (rg *rig) closedOf() []intake.Closed {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return append([]intake.Closed(nil), rg.closed...)
}

func (rg *rig) take(text string) (string, intake.TakeOutcome) {
	id, o := rg.reg.Take(context.Background(), intake.Pasted{From: admin, Private: true, Text: text})
	rg.reg.Wait()
	return id, o
}

func (rg *rig) wantReply(key texts.Key) {
	rg.t.Helper()
	got := rg.replies()
	if len(got) != 1 || got[0] != texts.EnglishText(key) {
		rg.t.Fatalf("replies %q, want the text of %s", got, key)
	}
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// seal does what the page does: HPKE to the request's key, info with the id and name.
func seal(t *testing.T, pub []byte, id, name string, value []byte) string {
	t.Helper()
	pk, err := ecdh.P256().NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	hk, err := hpke.NewDHKEMPublicKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := hpke.Seal(hk, hpke.HKDFSHA256(), hpke.AES128GCM(), []byte("ganglion-intake-v1\x00"+id+"\x00"+name), value)
	if err != nil {
		t.Fatal(err)
	}
	return "gsec1." + id + "." + b64u(ct)
}

// valueSink records what a SecretFunc got, as a copy, and how often it ran.
type valueSink struct {
	calls int
	id    string
	value string
	err   error
}

func (v *valueSink) fn(_ context.Context, id string, value []byte) error {
	v.calls++
	v.id, v.value = id, string(value)
	return v.err
}

type fixture struct {
	PrivateKey string `json:"private_key"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	Line       string `json:"line"`
}

// A gsec1 line sealed by the page's own script under Node.js opens to its value.
func TestTheFixtureFromThePageOpens(t *testing.T) {
	raw, err := os.ReadFile("testdata/seal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	scalar, err := hex.DecodeString(fx.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		t.Fatal(err)
	}
	rg := newRig(t)
	var v valueSink
	rg.reg.AddSecret(fx.ID, fx.Name, priv, v.fn)
	id, o := rg.take(fx.Line)
	if o != intake.LineTaken || id != fx.ID || v.calls != 1 || v.value != fx.Value || v.id != fx.ID {
		t.Fatalf("outcome %s id %q calls %d value %q", o, id, v.calls, v.value)
	}
	if got := rg.closedOf(); len(got) != 1 || got[0].Outcome != intake.OutcomeAnswered || got[0].Kind != intake.KindSecret {
		t.Fatalf("closed %+v", got)
	}
	if len(rg.replies()) != 0 {
		t.Fatalf("a reply to a good line: %q", rg.replies())
	}
}

func TestSecretRoundTrip(t *testing.T) {
	rg := newRig(t)
	var v valueSink
	req, err := rg.reg.OpenSecret(context.Background(), "shop_api_key", v.fn)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.PublicKey) != 65 || req.PublicKey[0] != 4 || len(req.ID) != 22 || req.Name != "shop_api_key" {
		t.Fatalf("request %+v", req)
	}
	if want := rg.clock.now().Add(10 * time.Minute); !req.Expires.Equal(want) {
		t.Errorf("expires %v, want %v", req.Expires, want)
	}
	line := seal(t, req.PublicKey, req.ID, "shop_api_key", []byte("hunter2"))
	if id, o := rg.take(line); o != intake.LineTaken || id != req.ID {
		t.Fatalf("outcome %s id %q", o, id)
	}
	if v.value != "hunter2" || v.calls != 1 {
		t.Fatalf("handler got %q, %d calls", v.value, v.calls)
	}
}

func TestOAuthRoundTrip(t *testing.T) {
	rg := newRig(t)
	var got intake.OAuthAnswer
	req, err := rg.reg.OpenOAuth(context.Background(), func(_ context.Context, a intake.OAuthAnswer) error { got = a; return nil })
	if err != nil {
		t.Fatal(err)
	}
	line := "gauth1." + req.ID + "." + b64u([]byte("c:o/de")) + "." + b64u([]byte("https://auth.example.com"))
	if id, o := rg.take(line); o != intake.LineTaken || id != req.ID {
		t.Fatalf("outcome %s id %q", o, id)
	}
	if got != (intake.OAuthAnswer{ID: req.ID, Code: "c:o/de", Issuer: "https://auth.example.com"}) {
		t.Fatalf("handler got %+v", got)
	}
}

func TestAnExpiredRequestIsClosedAndAnswered(t *testing.T) {
	rg := newRig(t)
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	line := seal(t, req.PublicKey, req.ID, "k", []byte("x"))
	rg.clock.add(10 * time.Minute)
	if _, o := rg.take(line); o != intake.LineExpired {
		t.Fatalf("outcome %s", o)
	}
	rg.wantReply(texts.ReplyIntakeExpired)
	if v.calls != 0 {
		t.Fatal("the handler ran")
	}
	if got := rg.closedOf(); len(got) != 1 || got[0].Outcome != intake.OutcomeExpired {
		t.Fatalf("closed %+v", got)
	}
}

func TestALineWorksOnce(t *testing.T) {
	rg := newRig(t)
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	line := seal(t, req.PublicKey, req.ID, "k", []byte("x"))
	if _, o := rg.take(line); o != intake.LineTaken {
		t.Fatalf("first %s", o)
	}
	if _, o := rg.take(line); o != intake.LineUnknown {
		t.Fatalf("second %s", o)
	}
	rg.wantReply(texts.ReplyIntakeUnknown)
	if v.calls != 1 {
		t.Fatalf("handler ran %d times", v.calls)
	}
}

// Abuse: a line from someone else, or from a group, touches no request and
// gets no answer; the admin's own line still works afterwards.
func TestLinesFromElsewhereTouchNothing(t *testing.T) {
	rg := newRig(t)
	rg.admins["telegram:user:2"] = false
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	line := seal(t, req.PublicKey, req.ID, "k", []byte("x"))
	ctx := context.Background()
	if _, o := rg.reg.Take(ctx, intake.Pasted{From: "telegram:user:2", Private: true, Text: line}); o != intake.LineNotAdmin {
		t.Fatalf("stranger: %s", o)
	}
	if _, o := rg.reg.Take(ctx, intake.Pasted{From: "telegram:user:9", Private: true, Text: line}); o != intake.LineNotAdmin {
		t.Fatalf("unknown person: %s", o)
	}
	if _, o := rg.reg.Take(ctx, intake.Pasted{From: admin, Private: false, Text: line}); o != intake.LineNotPrivate {
		t.Fatalf("group: %s", o)
	}
	if v.calls != 0 || len(rg.replies()) != 0 || len(rg.closedOf()) != 0 {
		t.Fatalf("something happened: calls %d replies %q closed %v", v.calls, rg.replies(), rg.closedOf())
	}
	if _, o := rg.take(line); o != intake.LineTaken || v.value != "x" {
		t.Fatalf("the admin's own line: %s %q", o, v.value)
	}
}

func TestAnAdminCheckThatFailsIsNoAdmin(t *testing.T) {
	reg := intake.New(func(context.Context, string) (bool, error) { return false, errors.New("db down") },
		func(context.Context, ...event.Outgoing) error { return nil },
		func(context.Context, string) (texts.Lang, error) { return texts.English, nil },
		func(string) (string, string, bool) { return "", "", false }, texts.Shipped())
	_, o := reg.Take(context.Background(), intake.Pasted{From: admin, Private: true, Text: "gsec1." + id22 + ".abcd"})
	if o != intake.LineNotAdmin {
		t.Fatalf("outcome %s", o)
	}
}

const id22 = "AAECAwQFBgcICQoLDA0ODw"

func TestUnknownRequest(t *testing.T) {
	rg := newRig(t)
	line := "gauth1." + id22 + "." + b64u([]byte("code"))
	if id, o := rg.take(line); o != intake.LineUnknown || id != id22 {
		t.Fatalf("outcome %s id %q", o, id)
	}
	rg.wantReply(texts.ReplyIntakeUnknown)
}

func TestTamperedCiphertextClosesTheRequest(t *testing.T) {
	rg := newRig(t)
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	line := seal(t, req.PublicKey, req.ID, "k", []byte("x"))
	i := len(line) - 5
	flip := map[byte]byte{'A': 'B'}
	c := line[i]
	n, ok := flip[c]
	if !ok {
		n = 'A'
	}
	bad := line[:i] + string(n) + line[i+1:]
	if _, o := rg.take(bad); o != intake.LineFailed {
		t.Fatalf("outcome %s", o)
	}
	rg.wantReply(texts.ReplyIntakeFailed)
	if v.calls != 0 {
		t.Fatal("the handler ran")
	}
	// The request is closed: the good line is no use now.
	if _, o := rg.take(line); o != intake.LineUnknown {
		t.Fatalf("after failing: %s", o)
	}
	if got := rg.closedOf(); len(got) != 1 || got[0].Outcome != intake.OutcomeFailed {
		t.Fatalf("closed %+v", got)
	}
}

func TestACiphertextSealedForAnotherNameFails(t *testing.T) {
	rg := newRig(t)
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	if _, o := rg.take(seal(t, req.PublicKey, req.ID, "other", []byte("x"))); o != intake.LineFailed || v.calls != 0 {
		t.Fatalf("outcome %s, %d calls", o, v.calls)
	}
}

// Abuse: a ciphertext moved to another request of the same name fails: the id is in info.
func TestACiphertextMovedToAnotherRequestFails(t *testing.T) {
	rg := newRig(t)
	var a, b valueSink
	ra, _ := rg.reg.OpenSecret(context.Background(), "k", a.fn)
	rb, _ := rg.reg.OpenSecret(context.Background(), "k", b.fn)
	moved := strings.Replace(seal(t, ra.PublicKey, ra.ID, "k", []byte("x")), ra.ID, rb.ID, 1)
	if _, o := rg.take(moved); o != intake.LineFailed || a.calls+b.calls != 0 {
		t.Fatalf("outcome %s", o)
	}
}

func TestAHandlerErrorFails(t *testing.T) {
	rg := newRig(t)
	v := valueSink{err: errors.New("store down")}
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	// The handler runs after Take returned: Take reports the line handed over.
	if _, o := rg.take(seal(t, req.PublicKey, req.ID, "k", []byte("x"))); o != intake.LineTaken {
		t.Fatalf("outcome %s", o)
	}
	rg.wantReply(texts.ReplyIntakeFailed)
	if got := rg.closedOf(); len(got) != 1 || got[0].Outcome != intake.OutcomeFailed {
		t.Fatalf("closed %+v", got)
	}
	if strings.Contains(rg.logs.String(), "store down") {
		t.Error("the handler's error is in the log")
	}
}

func TestALineOfTheOtherKindLeavesTheRequestOpen(t *testing.T) {
	rg := newRig(t)
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	if _, o := rg.take("gauth1." + req.ID + "." + b64u([]byte("code"))); o != intake.LineUnknown {
		t.Fatalf("outcome %s", o)
	}
	if _, o := rg.take(seal(t, req.PublicKey, req.ID, "k", []byte("x"))); o != intake.LineTaken {
		t.Fatalf("the secret line after it: %s", o)
	}
}

func TestRefusedLines(t *testing.T) {
	rg := newRig(t)
	if _, o := rg.take("gsec2." + id22 + ".abcd"); o != intake.LineVersion {
		t.Fatalf("version: %s", o)
	}
	rg.wantReply(texts.ReplyIntakeVersion)
	rg = newRig(t)
	if id, o := rg.take("gsec1." + id22 + ".!!"); o != intake.LineMalformed || id != "" {
		t.Fatalf("malformed: %s %q", o, id)
	}
	rg.wantReply(texts.ReplyIntakeMalformed)
}

func TestLimits(t *testing.T) {
	rg := newRig(t, intake.WithConfig(intake.Config{TTL: time.Minute, MaxOpen: 2}))
	ctx := context.Background()
	var v valueSink
	for range 2 {
		if _, err := rg.reg.OpenSecret(ctx, "k", v.fn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rg.reg.OpenOAuth(ctx, nil); !errors.Is(err, intake.ErrTooMany) {
		t.Fatalf("err %v", err)
	}
	// Expired ones are swept first.
	rg.clock.add(time.Minute)
	if _, err := rg.reg.OpenOAuth(ctx, nil); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if got := rg.closedOf(); len(got) != 2 {
		t.Fatalf("closed %+v", got)
	}
}

func TestNames(t *testing.T) {
	rg := newRig(t)
	for _, name := range []string{"", strings.Repeat("a", 129), "a\x00b", "a\nb", "\xff"} {
		if _, err := rg.reg.OpenSecret(context.Background(), name, nil); !errors.Is(err, intake.ErrName) {
			t.Errorf("%q: err %v", name, err)
		}
	}
	if _, err := rg.reg.OpenSecret(context.Background(), strings.Repeat("я", 64), nil); err != nil {
		t.Errorf("a 128-byte name: %v", err)
	}
}

func TestRunSweepsExpiredRequests(t *testing.T) {
	rg := newRig(t)
	if _, err := rg.reg.OpenOAuth(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	rg.clock.add(11 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.reg.Run(ctx) }()
	rg.after <- time.Time{} // one tick
	rg.after <- time.Time{} // returns once the first sweep is over
	if got := rg.closedOf(); len(got) != 1 || got[0].Outcome != intake.OutcomeExpired || got[0].Kind != intake.KindOAuth {
		t.Fatalf("closed %+v", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestIdsComeFromTheReader(t *testing.T) {
	want := bytes.Repeat([]byte{7}, 16)
	rg := newRig(t, intake.WithRandom(bytes.NewReader(append(want, want...))))
	req, err := rg.reg.OpenOAuth(context.Background(), nil)
	if err != nil || req.ID != b64u(want) {
		t.Fatalf("id %q err %v", req.ID, err)
	}
	if _, err := rg.reg.OpenOAuth(context.Background(), nil); err == nil {
		t.Fatal("a repeated id was accepted")
	}
}

// Abuse: neither the line, the code, the issuer nor the value is in the
// log, in a reply or in an error, over every outcome.
func TestNothingSecretIsLoggedOrReplied(t *testing.T) {
	rg := newRig(t)
	ctx := context.Background()
	const value = "VALUE-never-seen-0451"
	const code = "CODE-never-seen-0451"
	var v valueSink
	sreq, _ := rg.reg.OpenSecret(ctx, "k", v.fn)
	secretLine := seal(t, sreq.PublicKey, sreq.ID, "k", []byte(value))
	oreq, _ := rg.reg.OpenOAuth(ctx, func(context.Context, intake.OAuthAnswer) error { return errors.New("failed with " + code) })
	oauthLine := "gauth1." + oreq.ID + "." + b64u([]byte(code)) + "." + b64u([]byte("https://iss.example.com"))
	lines := []string{secretLine, secretLine, oauthLine, "gsec2." + id22 + ".SECRETPART-0451", "gsec1." + id22 + ".SECRETPART!!"}
	for _, l := range lines {
		rg.take(l)
		rg.reg.Take(ctx, intake.Pasted{From: "telegram:user:2", Private: true, Text: l})
		rg.reg.Take(ctx, intake.Pasted{From: admin, Private: false, Text: l})
	}
	var all strings.Builder
	all.WriteString(rg.logs.String())
	all.WriteString(strings.Join(rg.replies(), "\n"))
	for _, c := range rg.closedOf() {
		all.WriteString(c.ID + string(c.Kind) + string(c.Outcome))
	}
	for _, secret := range []string{value, code, "SECRETPART", "iss.example.com", b64u([]byte(value)), b64u([]byte(code)), secretLine[len("gsec1."+sreq.ID)+1:], "store down"} {
		if secret != "" && strings.Contains(all.String(), secret) {
			t.Errorf("%q is in the log, a reply or a hook", secret)
		}
	}
}

func TestMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	rg := newRig(t)
	var v valueSink
	req, _ := rg.reg.OpenSecret(context.Background(), "k", v.fn)
	if got := rec.Value(t, "ganglion.intake.open", nil); got != 1 {
		t.Fatalf("open = %v", got)
	}
	rg.reg.Take(context.Background(), intake.Pasted{From: "telegram:user:2", Private: true, Text: "gsec1." + id22 + ".abcd"})
	rg.take(seal(t, req.PublicKey, req.ID, "k", []byte("x")))
	rg.take("gsec1." + id22 + "." + b64u(make([]byte, 90)))
	for _, tc := range []struct {
		name string
		want map[string]string
		n    float64
	}{
		{"ganglion.intake.lines", map[string]string{"outcome": "not_admin"}, 1},
		{"ganglion.intake.lines", map[string]string{"outcome": "taken"}, 1},
		{"ganglion.intake.lines", map[string]string{"outcome": "unknown"}, 1},
		{"ganglion.intake.requests", map[string]string{"kind": "secret", "outcome": "answered"}, 1},
		{"ganglion.intake.open", nil, 0},
	} {
		if got := rec.Value(t, tc.name, tc.want); got != tc.n {
			t.Errorf("%s %v = %v, want %v", tc.name, tc.want, got, tc.n)
		}
	}
}

// A slow handler, such as an OAuth code exchange, runs off the caller's path:
// Take returns at once, the request is already closed, and the handler gets a
// context that the caller's end does not cancel.
func TestASlowHandlerDoesNotHoldTake(t *testing.T) {
	rg := newRig(t)
	release := make(chan struct{})
	got := make(chan error, 1)
	req, _ := rg.reg.OpenOAuth(context.Background(), func(ctx context.Context, _ intake.OAuthAnswer) error {
		<-release
		got <- ctx.Err()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	_, o := rg.reg.Take(ctx, intake.Pasted{From: admin, Private: true, Text: "gauth1." + req.ID + "." + b64u([]byte("c"))})
	cancel()
	if o != intake.LineTaken {
		t.Fatalf("outcome %s", o)
	}
	repeat := intake.Pasted{From: admin, Private: true, Text: "gauth1." + req.ID + "." + b64u([]byte("c"))}
	if _, o := rg.reg.Take(context.Background(), repeat); o != intake.LineUnknown {
		t.Fatalf("a repeat while the handler runs: %s", o)
	}
	close(release)
	rg.reg.Wait()
	if err := <-got; err != nil {
		t.Fatalf("the handler's context ended with the caller's: %v", err)
	}
	if c := rg.closedOf(); len(c) != 1 || c[0].Outcome != intake.OutcomeAnswered {
		t.Fatalf("closed %+v", c)
	}
}

// A line that is not the admin's own text message alone is refused with a
// fixed text, and its request stays open.
func TestALineNotAloneIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, p := range map[string]intake.Pasted{
		"caption":     {Caption: true},
		"forwarded":   {Forwarded: true},
		"words first": {},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newRig(t)
			v := valueSink{}
			req, _ := rg.reg.OpenSecret(ctx, "k", v.fn)
			line := seal(t, req.PublicKey, req.ID, "k", []byte("x"))
			p.From, p.Private, p.Text = admin, true, line
			if name == "words first" {
				p.Text = "here: " + line
			}
			if _, o := rg.reg.Take(ctx, p); o != intake.LineNotAlone {
				t.Fatalf("outcome %s", o)
			}
			rg.wantReply(texts.ReplyIntakeAlone)
			if _, o := rg.take(line); o != intake.LineTaken || v.calls != 1 {
				t.Fatalf("the request did not stay open: %s", o)
			}
		})
	}
}
