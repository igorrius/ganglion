package intake_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/texts"
)

const abuseValue = "abuse-value-31337"

// stored is one call of the fake store.
type stored struct {
	scope         secrets.Scope
	name, val, by string
}

// askRig is an Asker over a rig's registry, with fakes that record calls.
type askRig struct {
	*rig
	asker  *intake.Asker
	amu    sync.Mutex
	stores []stored
	puts   []event.Event
	dones  []event.SecretClosed
	store  error // what the store returns
}

func newAskRig(t *testing.T) *askRig {
	t.Helper()
	ar := &askRig{rig: newRig(t)}
	store := func(_ context.Context, scope secrets.Scope, name, value, by string) error {
		ar.amu.Lock()
		defer ar.amu.Unlock()
		ar.stores = append(ar.stores, stored{scope, name, value, by})
		return ar.store
	}
	enqueue := func(_ context.Context, m ...event.Outgoing) error {
		ar.mu.Lock()
		ar.sent = append(ar.sent, m...)
		ar.mu.Unlock()
		return nil
	}
	put := func(_ context.Context, e event.Event) error {
		ar.amu.Lock()
		ar.puts = append(ar.puts, e)
		ar.amu.Unlock()
		return nil
	}
	lang := func(context.Context, string) (texts.Lang, error) { return texts.English, nil }
	private := func(p string) (string, string, bool) {
		return "telegram", "telegram:" + p[len("telegram:user:"):], true
	}
	ar.asker = intake.NewAsker(ar.reg, store, enqueue, put, lang, private, texts.Shipped(),
		intake.WithAskerClock(ar.clock.now, time.UTC))
	return ar
}

func (ar *askRig) ask(k intake.SecretAsk) (intake.Asked, error) {
	return ar.asker.Ask(context.Background(), k, func(_ context.Context, c event.SecretClosed) {
		ar.amu.Lock()
		ar.dones = append(ar.dones, c)
		ar.amu.Unlock()
	})
}

func shopAsk() intake.SecretAsk {
	return intake.SecretAsk{Scope: secrets.ModuleScope("local/shop"), Name: "SHOP_KEY", Asking: intake.AskingModule, Who: "shop",
		Hosts: []string{"api.example.com", "cdn.example.com"}, Why: "to read orders", Admin: admin,
		Transport: "telegram", Conv: "telegram:1", Key: "turn1:1"}
}

func (ar *askRig) outgoing() []event.Outgoing {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return append([]event.Outgoing(nil), ar.sent...)
}

// pubOf reads the request's public key from the link in the card, as the page does.
func pubOf(t *testing.T, card string) []byte {
	t.Helper()
	i := strings.Index(card, "("+intake.DefaultPage+"#")
	if i < 0 {
		t.Fatalf("no link in the card %q", card)
	}
	raw := card[i+1+len(intake.DefaultPage)+1:]
	raw = raw[:strings.Index(raw, ")")]
	q, err := url.ParseQuery(raw)
	if err != nil || q.Get("mode") != "secret" {
		t.Fatalf("link fragment %q: %v", raw, err)
	}
	pk, err := base64.RawURLEncoding.DecodeString(q.Get("pk"))
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

func speak() texts.Speaker { return texts.Speaker{Lang: texts.English, Cat: texts.Shipped()} }

func TestAskSendsOneCard(t *testing.T) {
	ar := newAskRig(t)
	k := shopAsk()
	k.Tainted = true
	k.Why = "to read\n  orders"
	asked, err := ar.ask(k)
	if err != nil {
		t.Fatal(err)
	}
	out := ar.outgoing()
	if len(out) != 1 {
		t.Fatalf("sent %+v", out)
	}
	c := out[0]
	if c.Transport != "telegram" || c.Conv != "telegram:1" || !c.NoPreview || c.IdemKey != "intake:turn1:1:card" {
		t.Fatalf("card %+v", c)
	}
	if !asked.Expires.Equal(ar.clock.now().Add(10 * time.Minute)) {
		t.Errorf("expires %v", asked.Expires)
	}
	for _, w := range []string{`SHOP\_KEY`, "shop", "api.example.com, cdn.example.com", "«to read orders»", "12:10",
		texts.EnglishText(texts.SecretCardTainted), "(" + intake.DefaultPage + "#", "mode=secret", asked.ID} {
		if !strings.Contains(c.Text, w) {
			t.Errorf("the card lacks %q: %q", w, c.Text)
		}
	}
	// The same key twice is the same card.
	if _, err := ar.ask(k); err != nil {
		t.Fatal(err)
	}
	if out := ar.outgoing(); len(out) != 2 || out[1].IdemKey != out[0].IdemKey {
		t.Fatalf("keys %q, %q", out[0].IdemKey, out[len(out)-1].IdemKey)
	}
}

func TestAskWithoutTaintHasNoTaintedLine(t *testing.T) {
	ar := newAskRig(t)
	if _, err := ar.ask(shopAsk()); err != nil {
		t.Fatal(err)
	}
	if c := ar.outgoing()[0]; strings.Contains(c.Text, texts.EnglishText(texts.SecretCardTainted)) {
		t.Fatalf("a tainted line on a clean card: %q", c.Text)
	}
}

func TestPastedLineStoresAndFolds(t *testing.T) {
	ar := newAskRig(t)
	k := shopAsk()
	asked, err := ar.ask(k)
	if err != nil {
		t.Fatal(err)
	}
	card := ar.outgoing()[0]
	line := seal(t, pubOf(t, card.Text), asked.ID, k.Name, []byte(abuseValue))
	if _, o := ar.take(line); o != intake.LineTaken {
		t.Fatalf("outcome %s", o)
	}
	if len(ar.stores) != 1 || ar.stores[0] != (stored{k.Scope, k.Name, abuseValue, admin}) {
		t.Fatalf("stores %+v", ar.stores)
	}
	out := ar.outgoing()
	if len(out) != 2 {
		t.Fatalf("sent %+v", out)
	}
	fold := out[1]
	wantText := speak().Text(texts.SecretLineSet, texts.Vars{"name": texts.Escape(k.Name),
		"whose": speak().Text(texts.SecretWhoseModule, texts.Vars{"who": "shop"}), "time": "12:00"})
	if fold.EditOf != card.IdemKey || fold.Text != wantText || fold.Folded != card.Text || fold.Conv != card.Conv ||
		strings.Contains(fold.Text, "\n") {
		t.Fatalf("fold %+v, want text %q", fold, wantText)
	}
	if len(ar.puts) != 1 {
		t.Fatalf("puts %+v", ar.puts)
	}
	e := ar.puts[0]
	var sc event.SecretClosed
	if err := json.Unmarshal(e.Payload, &sc); err != nil {
		t.Fatal(err)
	}
	if e.Kind != event.KindSecretClosed || e.Source != event.SourceIntake || e.Conv != "telegram:1" || e.IdemKey != "intake:"+asked.ID+":closed" ||
		sc.Outcome != event.SecretSet || sc.Name != k.Name || sc.Scope != string(k.Scope) || sc.Request != asked.ID ||
		sc.Principal != admin || sc.Transport != "telegram" {
		t.Fatalf("event %+v, payload %+v", e, sc)
	}
	if len(ar.dones) != 1 || ar.dones[0].Outcome != event.SecretSet {
		t.Fatalf("dones %+v", ar.dones)
	}
}

func TestNoConvWritesNoNote(t *testing.T) {
	ar := newAskRig(t)
	k := shopAsk()
	k.Conv = ""
	asked, _ := ar.ask(k)
	line := seal(t, pubOf(t, ar.outgoing()[0].Text), asked.ID, k.Name, []byte("v"))
	ar.take(line)
	if len(ar.puts) != 0 || len(ar.dones) != 1 {
		t.Fatalf("puts %d dones %d", len(ar.puts), len(ar.dones))
	}
}

func TestExpiryFoldsAndNotes(t *testing.T) {
	ar := newAskRig(t)
	k := shopAsk()
	asked, _ := ar.ask(k)
	line := seal(t, pubOf(t, ar.outgoing()[0].Text), asked.ID, k.Name, []byte("v"))
	ar.clock.add(11 * time.Minute)
	if _, o := ar.take(line); o != intake.LineExpired {
		t.Fatalf("outcome %s", o)
	}
	if len(ar.stores) != 0 {
		t.Fatalf("stored after expiry: %+v", ar.stores)
	}
	var fold event.Outgoing
	for _, m := range ar.outgoing() {
		if m.EditOf != "" {
			fold = m
		}
	}
	want := speak().Text(texts.SecretLineExpired, texts.Vars{"name": texts.Escape(k.Name),
		"whose": speak().Text(texts.SecretWhoseModule, texts.Vars{"who": "shop"}), "time": "12:11"})
	if fold.Text != want {
		t.Fatalf("fold %q, want %q", fold.Text, want)
	}
	var sc event.SecretClosed
	if len(ar.puts) != 1 || json.Unmarshal(ar.puts[0].Payload, &sc) != nil || sc.Outcome != event.SecretExpired {
		t.Fatalf("puts %+v", ar.puts)
	}
	if len(ar.dones) != 1 || ar.dones[0].Outcome != event.SecretExpired {
		t.Fatalf("dones %+v", ar.dones)
	}
}

func TestStoreFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		err error
		key texts.Key
	}{
		"failed": {errors.New("disk full"), texts.SecretLineFailed},
		"locked": {errors.Join(errors.New("vault"), intake.ErrStoreLocked), texts.SecretLineLocked},
	} {
		t.Run(name, func(t *testing.T) {
			ar := newAskRig(t)
			ar.store = tc.err
			k := shopAsk()
			asked, _ := ar.ask(k)
			line := seal(t, pubOf(t, ar.outgoing()[0].Text), asked.ID, k.Name, []byte(abuseValue))
			ar.take(line)
			var fold event.Outgoing
			for _, m := range ar.outgoing() {
				if m.EditOf != "" {
					fold = m
				}
			}
			want := speak().Text(tc.key, texts.Vars{"name": texts.Escape(k.Name),
				"whose": speak().Text(texts.SecretWhoseModule, texts.Vars{"who": "shop"}), "time": "12:00"})
			if fold.EditOf == "" || fold.Text != want {
				t.Fatalf("fold %+v, want %q", fold, want)
			}
			if len(ar.dones) != 1 || ar.dones[0].Outcome != event.SecretFailed {
				t.Fatalf("dones %+v", ar.dones)
			}
			if strings.Contains(fold.Text+fold.Folded, abuseValue) || strings.Contains(ar.logs.String(), abuseValue) {
				t.Fatal("the value leaked")
			}
		})
	}
}

func TestPageCheckFailureRefusesTheAsk(t *testing.T) {
	for _, pe := range []error{intake.ErrPageChanged, intake.ErrPageUnchecked} {
		ar := newAskRig(t)
		ar.page = pe
		_, err := ar.ask(shopAsk())
		if !errors.Is(err, pe) {
			t.Fatalf("err %v, want %v", err, pe)
		}
		out := ar.outgoing()
		if len(out) != 1 || out[0].Text != speak().Text(texts.SecretStopped, texts.Vars{"name": texts.Escape("SHOP_KEY")}) ||
			out[0].Conv != "telegram:1" || strings.Contains(out[0].Text, "mode=secret") {
			t.Fatalf("sent %+v", out)
		}
	}
}

func TestAskValidation(t *testing.T) {
	for name, mut := range map[string]func(*intake.SecretAsk){
		"no scope":   func(k *intake.SecretAsk) { k.Scope = "" },
		"bad name":   func(k *intake.SecretAsk) { k.Name = "a b" },
		"empty name": func(k *intake.SecretAsk) { k.Name = "" },
		"no admin":   func(k *intake.SecretAsk) { k.Admin = "" },
	} {
		t.Run(name, func(t *testing.T) {
			ar := newAskRig(t)
			k := shopAsk()
			mut(&k)
			if _, err := ar.ask(k); !errors.Is(err, intake.ErrAsk) {
				t.Fatalf("err %v", err)
			}
			if len(ar.outgoing()) != 0 {
				t.Fatalf("sent %+v", ar.outgoing())
			}
		})
	}
}

// Abuse: the value is in the store and nowhere else; a hostile reason cannot
// add a link or a line to the card.
func TestAbuseValueNeverLeavesTheStore(t *testing.T) {
	ar := newAskRig(t)
	k := shopAsk()
	k.Why = "[click](https://evil.example)\nand then more"
	asked, _ := ar.ask(k)
	card := ar.outgoing()[0]
	if strings.Contains(card.Text, "[click](") || !strings.Contains(card.Text, `\[click\](https://evil.example)`) {
		t.Fatalf("the reason was not escaped: %q", card.Text)
	}
	if n := strings.Count(card.Text, "](") - strings.Count(card.Text, `\](`); n != 1 { // only the node's own link
		t.Fatalf("%d links in the card %q", n, card.Text)
	}
	var whyLine string
	for l := range strings.SplitSeq(card.Text, "\n") {
		if strings.Contains(l, "click") {
			whyLine = l
		}
	}
	if !strings.Contains(whyLine, "and then more") {
		t.Fatalf("the reason is not on one line: %q", card.Text)
	}
	line := seal(t, pubOf(t, card.Text), asked.ID, k.Name, []byte(abuseValue))
	ar.take(line)
	if len(ar.stores) != 1 || ar.stores[0].val != abuseValue {
		t.Fatalf("stores %+v", ar.stores)
	}
	for _, m := range ar.outgoing() {
		if strings.Contains(m.Text, abuseValue) || strings.Contains(m.Folded, abuseValue) {
			t.Fatalf("the value is in %+v", m)
		}
	}
	for _, e := range ar.puts {
		if strings.Contains(string(e.Payload), abuseValue) {
			t.Fatalf("the value is in an event %s", e.Payload)
		}
	}
	for _, d := range ar.dones {
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), abuseValue) {
			t.Fatalf("the value is in the done argument %s", raw)
		}
	}
	if strings.Contains(ar.logs.String(), abuseValue) {
		t.Fatal("the value is in the log")
	}
}
