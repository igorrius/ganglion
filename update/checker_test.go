package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
)

// fakeLatest is a source that answers from a function and counts calls.
type fakeLatest struct {
	mu    sync.Mutex
	calls int
	reply func() (Release, error)
}

func (f *fakeLatest) Latest(context.Context) (Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.reply()
}

func (f *fakeLatest) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func release(t *testing.T, tag string) Release {
	t.Helper()
	v, err := ParseVersion(tag)
	if err != nil {
		t.Fatal(err)
	}
	return Release{Version: v, Tag: tag, Published: time.Date(2026, 11, 15, 23, 30, 0, 0, time.UTC),
		Page: "https://github.com/igorrius/ganglion/releases/tag/" + tag}
}

// checkKit is a folder, fake admins, an outbox, a source and an after the
// test fires: each wait is announced on asked.
type checkKit struct {
	t       *testing.T
	dir     string
	src     *fakeLatest
	sent    []event.Outgoing
	enqErr  error
	asked   chan time.Duration
	fire    chan time.Time
	logs    *bytes.Buffer
	rec     *telemetrytest.Recorder
	catalog *texts.Catalog
}

func newCheckKit(t *testing.T, tag string) *checkKit {
	t.Helper()
	rec := telemetrytest.Install(t)
	cat, err := texts.New()
	if err != nil {
		t.Fatal(err)
	}
	k := &checkKit{t: t, dir: filepath.Join(t.TempDir(), "releases"), asked: make(chan time.Duration), fire: make(chan time.Time),
		logs: &bytes.Buffer{}, rec: rec, catalog: cat}
	k.src = &fakeLatest{reply: func() (Release, error) { return release(t, tag), nil }}
	return k
}

func (k *checkKit) checker(running string) *Checker {
	admins := alert.Admins{
		List:    func(context.Context) ([]string, error) { return []string{"telegram:user:1", "telegram:user:2"}, nil },
		Private: func(p string) (string, string, bool) { return "telegram", "chat-" + p, true },
	}
	enqueue := func(_ context.Context, msgs ...event.Outgoing) error {
		if k.enqErr != nil {
			return k.enqErr
		}
		k.sent = append(k.sent, msgs...)
		return nil
	}
	lang := func(_ context.Context, p string) (texts.Lang, error) {
		if p == "telegram:user:2" {
			return "ru", nil
		}
		return texts.English, nil
	}
	after := func(d time.Duration) <-chan time.Time { k.asked <- d; return k.fire }
	return NewChecker(k.src, k.dir, running, admins, enqueue, lang, k.catalog, slog.New(slog.NewTextHandler(k.logs, nil)),
		WithCheckAfter(after), WithCheckClock(func() time.Time { return testNow }))
}

// run starts the checker and returns a function that fires the next wait
// and the duration that was waited for it.
func (k *checkKit) run(c *Checker) (step func() time.Duration, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	var waited time.Duration
	step = func() time.Duration {
		k.t.Helper()
		select {
		case waited = <-k.asked:
		case <-time.After(5 * time.Second):
			k.t.Fatal("the checker did not wait")
		}
		return waited
	}
	stop = func() {
		cancel()
		if err := <-done; err != nil {
			k.t.Fatal(err)
		}
	}
	return step, stop
}

// next fires the wait just announced and waits until the checker is
// waiting again, so the check in between is done.
func (k *checkKit) next(step func() time.Duration) time.Duration {
	k.t.Helper()
	k.fire <- time.Time{}
	return step()
}

func (k *checkKit) notified() (notified, bool) {
	k.t.Helper()
	b, err := os.ReadFile(filepath.Join(k.dir, NotifiedFile))
	if err != nil {
		return notified{}, false
	}
	var n notified
	if err := json.Unmarshal(b, &n); err != nil {
		k.t.Fatal(err)
	}
	return n, true
}

func TestCheckerChecksAfterTenMinutesThenDaily(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	if d := step(); d != 10*time.Minute {
		t.Fatalf("the first wait is %v", d)
	}
	if k.src.count() != 0 {
		t.Fatal("it asked before the first wait ended")
	}
	if d := k.next(step); d != 24*time.Hour || k.src.count() != 1 {
		t.Fatalf("the next wait %v after %d checks", d, k.src.count())
	}
	if d := k.next(step); d != 24*time.Hour || k.src.count() != 2 {
		t.Fatalf("the next wait %v after %d checks", d, k.src.count())
	}
}

func TestCheckerTellsOncePerVersionInEachLanguageAndRecordsIt(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	k.next(step) // the first check
	if len(k.sent) != 2 || k.sent[0].IdemKey != "update:available:v0.3.3:telegram:user:1" {
		t.Fatalf("%+v", k.sent)
	}
	en, ru := k.sent[0].Text, k.sent[1].Text
	for _, want := range []string{"v0.3.3", "2026-11-15", "https://github.com/igorrius/ganglion/releases/tag/v0.3.3", "/update install v0.3.3"} {
		if !strings.Contains(en, want) || !strings.Contains(ru, want) {
			t.Fatalf("%q lacks %q:\n%s\n%s", en, want, en, ru)
		}
	}
	if !strings.Contains(en, "A new release is out") || !strings.Contains(ru, "Вышел новый релиз") {
		t.Fatalf("languages: %q %q", en, ru)
	}
	n, ok := k.notified()
	if !ok || n.Version != "v0.3.3" || !n.At.Equal(testNow) {
		t.Fatalf("record %+v %v", n, ok)
	}
	k.next(step) // the same version a day later
	if len(k.sent) != 2 {
		t.Fatalf("told again: %d messages", len(k.sent))
	}
	if got := k.rec.Value(t, "ganglion.update.checks", map[string]string{"outcome": "newer"}); got != 2 {
		t.Fatalf("newer checks = %v", got)
	}
}

func TestCheckerDoesNotTellAgainAfterARestartAndTellsAgainForANewerOne(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	step, stop := k.run(k.checker("v0.3.0"))
	step()
	k.next(step)
	stop()
	if len(k.sent) != 2 {
		t.Fatalf("%d messages", len(k.sent))
	}

	// A restart: a new checker over the same folder.
	step, stop = k.run(k.checker("v0.3.0"))
	step()
	k.next(step)
	if len(k.sent) != 2 {
		t.Fatalf("told again after a restart: %d messages", len(k.sent))
	}
	k.src.reply = func() (Release, error) { return release(t, "v0.3.4"), nil }
	k.next(step)
	stop()
	if len(k.sent) != 4 || !strings.Contains(k.sent[2].Text, "v0.3.4") {
		t.Fatalf("%+v", k.sent)
	}
	if n, _ := k.notified(); n.Version != "v0.3.4" {
		t.Fatalf("record %+v", n)
	}
}

func TestCheckerSaysNothingWhenTheNodeIsCurrent(t *testing.T) {
	k := newCheckKit(t, "v0.3.0")
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	k.next(step)
	if len(k.sent) != 0 {
		t.Fatalf("%+v", k.sent)
	}
	if _, ok := k.notified(); ok {
		t.Fatal("a record without a notice")
	}
	if got := k.rec.Value(t, "ganglion.update.checks", map[string]string{"outcome": "current"}); got != 1 {
		t.Fatalf("current checks = %v", got)
	}
}

func TestCheckerKeepsTheVersionForALaterTryWhenTheAdminsWereNotTold(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	k.enqErr = errors.New("outbox down")
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	k.next(step)
	if _, ok := k.notified(); ok {
		t.Fatal("recorded a version nobody was told")
	}
	k.enqErr = nil
	k.next(step)
	if len(k.sent) != 2 {
		t.Fatalf("%d messages", len(k.sent))
	}
}

func TestCheckerUnreadableRecordMeansNothingWasTold(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	if err := os.MkdirAll(k.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k.dir, NotifiedFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	k.next(step)
	if len(k.sent) != 2 || !strings.Contains(k.logs.String(), "not readable") {
		t.Fatalf("%d messages, log %q", len(k.sent), k.logs)
	}
	if n, _ := k.notified(); n.Version != "v0.3.3" {
		t.Fatalf("record %+v", n)
	}
}

func TestCheckerOnABuildWithoutAReleaseVersionChecksNothing(t *testing.T) {
	for _, running := range []string{"dev", "v0.3.0-4-gabc1234", "v0.3.0-dirty", ""} {
		k := newCheckKit(t, "v0.3.3")
		c := k.checker(running)
		if err := c.Run(context.Background()); err != nil { // returns at once: no wait to fire
			t.Fatal(err)
		}
		if k.src.count() != 0 || len(k.sent) != 0 {
			t.Fatalf("%q: asked %d times, sent %d", running, k.src.count(), len(k.sent))
		}
		if !strings.Contains(k.logs.String(), "level=INFO") || strings.Count(k.logs.String(), "without a release version") != 1 {
			t.Fatalf("%q: log %q", running, k.logs)
		}
		if got := k.rec.Value(t, "ganglion.update.checks", map[string]string{"outcome": "skipped"}); got != 1 {
			t.Fatalf("%q: skipped = %v", running, got)
		}
	}
}

func TestCheckerFailedCheckLogsAndKeepsGoing(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	fail := true
	k.src.reply = func() (Release, error) {
		if fail {
			return Release{}, ErrFetch
		}
		return release(t, "v0.3.3"), nil
	}
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	if d := k.next(step); d != 24*time.Hour {
		t.Fatalf("after a failed check the wait is %v", d)
	}
	if len(k.sent) != 0 || !strings.Contains(k.logs.String(), "level=WARN") {
		t.Fatalf("%d messages, log %q", len(k.sent), k.logs)
	}
	if got := k.rec.Value(t, "ganglion.update.checks", map[string]string{"outcome": "failed"}); got != 1 {
		t.Fatalf("failed = %v", got)
	}
	fail = false
	k.next(step)
	if len(k.sent) != 2 {
		t.Fatalf("%d messages after the source recovered", len(k.sent))
	}
}

func TestCheckerStopsWithTheContext(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	step, stop := k.run(k.checker("v0.3.0"))
	step()
	stop() // while waiting for the first check
	if k.src.count() != 0 {
		t.Fatal("it checked after the stop")
	}
}

// A release page that holds an instruction reaches no message: only the
// version, the date and the link the node builds do (R-UPD-7).
func TestCheckerMessageHoldsNothingOfTheReleasePage(t *testing.T) {
	f := newFake(t)
	f.add("v0.3.3", false, false)
	k := newCheckKit(t, "v0.3.3")
	k.src = &fakeLatest{reply: func() (Release, error) { return f.source().Latest(context.Background()) }}
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	k.next(step)
	if len(k.sent) != 2 {
		t.Fatalf("%d messages", len(k.sent))
	}
	for _, m := range k.sent {
		if strings.Contains(m.Text, "Ignore") || strings.Contains(m.Text, "instructions") || strings.Contains(m.Text, "v9.9.9") {
			t.Fatalf("the release's text reached the message: %q", m.Text)
		}
	}
	if strings.Contains(k.logs.String(), "Ignore") {
		t.Fatalf("the release's text reached the log: %q", k.logs)
	}
}

func TestCheckerRecordIsPrivateAndLeavesNoTemporaryFile(t *testing.T) {
	k := newCheckKit(t, "v0.3.3")
	step, stop := k.run(k.checker("v0.3.0"))
	defer stop()
	step()
	k.next(step)
	if fi, err := os.Stat(k.dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder %v %v", fi, err)
	}
	entries, err := os.ReadDir(k.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != NotifiedFile {
		t.Fatalf("%v %v", entries, err)
	}
}
