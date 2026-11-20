package update

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
)

// watchKit is a store with pending state, fake admins, an outbox and an
// after the test fires.
type watchKit struct {
	*kit
	sent    []event.Outgoing
	enqErr  error
	fire    chan time.Time
	waited  []time.Duration
	langs   map[string]texts.Lang
	catalog *texts.Catalog
	rec     *telemetrytest.Recorder
}

func newWatchKit(t *testing.T) *watchKit {
	t.Helper()
	rec := telemetrytest.Install(t) // before the store: instruments are made by then
	cat, err := texts.New()
	if err != nil {
		t.Fatal(err)
	}
	return &watchKit{kit: newKit(t), rec: rec, fire: make(chan time.Time, 1), catalog: cat,
		langs: map[string]texts.Lang{"telegram:user:2": "ru"}}
}

func (w *watchKit) watch(running string) *Watch {
	admins := alert.Admins{
		List: func(context.Context) ([]string, error) { return []string{"telegram:user:1", "telegram:user:2"}, nil },
		Private: func(p string) (string, string, bool) {
			return "telegram", "chat-" + p, true
		},
	}
	enqueue := func(_ context.Context, msgs ...event.Outgoing) error {
		if w.enqErr != nil {
			return w.enqErr
		}
		w.sent = append(w.sent, msgs...)
		return nil
	}
	lang := func(_ context.Context, p string) (texts.Lang, error) {
		if l, ok := w.langs[p]; ok {
			return l, nil
		}
		return texts.English, nil
	}
	after := func(d time.Duration) <-chan time.Time { w.waited = append(w.waited, d); return w.fire }
	return NewWatch(w.store, running, admins, enqueue, lang, w.catalog, slog.New(slog.DiscardHandler), WithAfter(after))
}

func (w *watchKit) pending(version string) {
	w.t.Helper()
	if _, err := w.install(version, "v0.2.1"); err != nil {
		w.t.Fatal(err)
	}
}

func (w *watchKit) state() State {
	w.t.Helper()
	st, err := w.store.State()
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

func TestWatchConfirmsAfterTwoMinutesAndTellsOnce(t *testing.T) {
	w := newWatchKit(t)
	w.pending("v0.3.0")
	done := make(chan error, 1)
	go func() { done <- w.watch("v0.3.0").Run(context.Background()) }()
	if st := w.state(); st.Pending == nil { // still waiting: nothing is cleared before the time
		t.Fatalf("%+v", st)
	}
	w.fire <- time.Time{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := w.state()
	if st.Pending != nil || st.Confirmed != "v0.3.0" || len(w.waited) != 1 || w.waited[0] != 2*time.Minute {
		t.Fatalf("%+v %v", st, w.waited)
	}
	// At once: started, in each admin's language; after the time: confirmed.
	if len(w.sent) != 4 || !strings.Contains(w.sent[0].Text, "v0.3.0 has started") || !strings.Contains(w.sent[1].Text, "запущена") {
		t.Fatalf("%+v", w.sent)
	}
	if !strings.HasPrefix(w.sent[2].IdemKey, "update:confirmed:v0.3.0:") || !strings.Contains(w.sent[2].Text, "now runs v0") ||
		!strings.Contains(w.sent[3].Text, "Узел") {
		t.Fatalf("%+v", w.sent)
	}
	for name, want := range map[string]float64{"ganglion.update.confirmations": 1} {
		if got := w.rec.Value(t, name, nil); got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
	if got := w.rec.Value(t, "ganglion.update.installs", map[string]string{"outcome": "installed"}); got != 1 {
		t.Fatalf("installs = %v", got)
	}
	// A restart of the confirmed version: nothing pending, nothing said.
	w.sent = nil
	if err := w.watch("v0.3.0").Run(context.Background()); err != nil || len(w.sent) != 0 {
		t.Fatalf("%v %+v", err, w.sent)
	}
}

func TestWatchDoesNotConfirmWhenTheContextEndsFirst(t *testing.T) {
	w := newWatchKit(t)
	w.pending("v0.3.0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.watch("v0.3.0").Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Only the start was told; nothing was confirmed.
	if st := w.state(); st.Pending == nil || st.Confirmed != "" || len(w.sent) != 2 || !strings.HasPrefix(w.sent[0].IdemKey, "update:started:") {
		t.Fatalf("%+v %+v", st, w.sent)
	}
}

func TestWatchTellsTheVersionAfterARollbackOnce(t *testing.T) {
	w := newWatchKit(t)
	w.pending("v0.3.0")
	w.fire <- time.Time{}
	if err := w.watch("v0.3.0").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Rollback(context.Background()); err != nil { // to the image's binary
		t.Fatal(err)
	}
	w.sent = nil
	if err := w.watch("v0.2.1").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.sent) != 2 || !strings.Contains(w.sent[0].Text, "now runs v0.2.1") || w.state().Confirmed != "v0.2.1" {
		t.Fatalf("%+v %+v", w.sent, w.state())
	}
	w.sent = nil
	if err := w.watch("v0.2.1").Run(context.Background()); err != nil || len(w.sent) != 0 {
		t.Fatalf("told twice: %v %+v", err, w.sent)
	}
}

func TestWatchTellsTheImagesVersionAfterTheReleasesWereDropped(t *testing.T) {
	w := newWatchKit(t)
	if err := w.store.writeState(State{Confirmed: "v0.3.0"}); err != nil { // as the launcher leaves it
		t.Fatal(err)
	}
	if err := w.watch("v0.4.0").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.sent) != 2 || !strings.Contains(w.sent[0].Text, "now runs v0.4.0") || w.state().Confirmed != "v0.4.0" {
		t.Fatalf("%+v %+v", w.sent, w.state())
	}
}

func TestWatchOfABuildWithoutAReleaseVersionTellsNothing(t *testing.T) {
	w := newWatchKit(t)
	if err := w.store.writeState(State{Confirmed: "v0.3.0"}); err != nil {
		t.Fatal(err)
	}
	if err := w.watch("dev").Run(context.Background()); err != nil || len(w.sent) != 0 || w.state().Confirmed != "v0.3.0" {
		t.Fatalf("%v %+v %+v", err, w.sent, w.state())
	}
}

func TestWatchLeavesAnotherVersionsPendingAlone(t *testing.T) {
	w := newWatchKit(t)
	w.pending("v0.3.0")
	if err := w.watch("v0.2.1").Run(context.Background()); err != nil || len(w.waited) != 0 || w.state().Pending == nil {
		t.Fatalf("%v %v", err, w.waited)
	}
}

func TestWatchTellsAFallbackOnceInEachAdminsLanguageAndClearsIt(t *testing.T) {
	w := newWatchKit(t)
	w.pending("v0.3.0")
	if _, err := w.store.mutate(func(st *State) bool {
		st.Fallback = &Fallback{From: "v0.3.0", To: "", RestoredDB: true, Reason: ReasonFailed, At: testNow}
		st.Active, st.Pending = "", nil
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.watch("v0.2.1").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.sent) != 2 || w.state().Fallback != nil {
		t.Fatalf("%+v %+v", w.sent, w.state())
	}
	en, ru := w.sent[0], w.sent[1]
	if !strings.Contains(en.Text, "did not start in two tries") || !strings.Contains(en.Text, "restored") || !strings.Contains(en.Text, "version of the image") {
		t.Fatalf("%q", en.Text)
	}
	if !strings.Contains(ru.Text, "не запустилась") || !strings.HasPrefix(en.IdemKey, "update:fallback:v0.3.0:") {
		t.Fatalf("%q %q", ru.Text, en.IdemKey)
	}
	if got := w.rec.Value(t, "ganglion.update.fallbacks", map[string]string{"reason": "failed"}); got != 1 {
		t.Fatalf("fallbacks = %v", got)
	}
	w.sent = nil
	if err := w.watch("v0.2.1").Run(context.Background()); err != nil || len(w.sent) != 0 {
		t.Fatalf("told twice: %v %+v", err, w.sent)
	}
}

func TestWatchKeepsAFallbackItCouldNotTell(t *testing.T) {
	w := newWatchKit(t)
	if _, err := w.store.mutate(func(st *State) bool {
		st.Fallback = &Fallback{From: "v0.3.0", Reason: ReasonNoExec, At: testNow}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	w.enqErr = errors.New("db down")
	if err := w.watch("").Run(context.Background()); err != nil || w.state().Fallback == nil {
		t.Fatalf("%v %+v", err, w.state())
	}
	w.enqErr = nil
	if err := w.watch("").Run(context.Background()); err != nil || w.state().Fallback != nil || len(w.sent) != 2 {
		t.Fatalf("%v %+v", err, w.sent)
	}
	if !strings.Contains(w.sent[0].Text, "does not allow running programs") {
		t.Fatalf("%q", w.sent[0].Text)
	}
}

func TestWatchWithAnUnreadableStateStartsNothingAndDoesNotFail(t *testing.T) {
	w := newWatchKit(t)
	if _, err := w.store.mutate(func(*State) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(w.store.Dir()+"/"+StateFile, `{"active":"../x"}`); err != nil {
		t.Fatal(err)
	}
	if err := w.watch("v0.3.0").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
