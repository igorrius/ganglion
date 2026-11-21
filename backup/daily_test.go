package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
)

// scriptedRunner answers each run from a list and records the result like
// Backup.Run does.
type scriptedRunner struct {
	mu    sync.Mutex
	dir   string
	steps []Result
	runs  int
	// requests are the ids the runs were asked for, "" for a daily one.
	requests []string
	// refuse, when set, is what a run fails with before it starts.
	refuse error
}

func (r *scriptedRunner) Run(ctx context.Context) (Result, error) { return r.RunRequest(ctx, "") }

func (r *scriptedRunner) RunRequest(_ context.Context, request string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refuse != nil {
		return Result{}, r.refuse
	}
	res := r.steps[min(r.runs, len(r.steps)-1)]
	res.Request = request
	r.requests = append(r.requests, request)
	r.runs++
	if err := writeResult(r.dir, res); err != nil {
		return res, err
	}
	if res.OK {
		return res, nil
	}
	return res, errors.New(res.Reason)
}

func (r *scriptedRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

type dailyKit struct {
	t     *testing.T
	dir   string
	run   *scriptedRunner
	sent  []event.Outgoing
	asked chan time.Duration
	fire  chan time.Time
	// tick fires the wait between two looks for a request file.
	tick chan time.Time
	now  time.Time
	loc  *time.Location
	logs *bytes.Buffer
	rec  *telemetrytest.Recorder
}

func newDailyKit(t *testing.T, steps ...Result) *dailyKit {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return &dailyKit{t: t, dir: dir, run: &scriptedRunner{dir: dir, steps: steps}, asked: make(chan time.Duration), tick: make(chan time.Time),
		fire: make(chan time.Time), now: time.Date(2026, 11, 17, 10, 0, 0, 0, loc), loc: loc, logs: &bytes.Buffer{},
		rec: telemetrytest.Install(t)}
}

func (k *dailyKit) daily() *Daily {
	cat, err := texts.New()
	if err != nil {
		k.t.Fatal(err)
	}
	admins := alert.Admins{
		List:    func(context.Context) ([]string, error) { return []string{"telegram:user:1", "telegram:user:2"}, nil },
		Private: func(p string) (string, string, bool) { return "telegram", "chat-" + p, true },
	}
	enqueue := func(_ context.Context, msgs ...event.Outgoing) error { k.sent = append(k.sent, msgs...); return nil }
	lang := func(_ context.Context, p string) (texts.Lang, error) {
		if p == "telegram:user:2" {
			return "ru", nil
		}
		return texts.English, nil
	}
	after := func(d time.Duration) <-chan time.Time { k.asked <- d; return k.fire }
	poll := func(time.Duration) <-chan time.Time { return k.tick }
	return NewDaily(k.run, k.dir, 3*time.Hour, admins, enqueue, lang, cat, slog.New(slog.NewTextHandler(k.logs, nil)),
		WithDailyAfter(after), WithDailyPoll(poll), WithDailyClock(func() time.Time { return k.now }), WithDailyLocation(k.loc))
}

func (k *dailyKit) start(d *Daily) (step func() time.Duration, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	step = func() time.Duration {
		k.t.Helper()
		select {
		case w := <-k.asked:
			return w
		case <-time.After(5 * time.Second):
			k.t.Fatal("the component did not wait")
			return 0
		}
	}
	stop = func() {
		cancel()
		if err := <-done; err != nil {
			k.t.Fatal(err)
		}
	}
	return step, stop
}

// next fires the wait just announced, moves the clock a day, and waits for
// the next announced wait.
func (k *dailyKit) next(step func() time.Duration) time.Duration {
	k.t.Helper()
	k.now = k.now.Add(24 * time.Hour)
	k.fire <- time.Time{}
	return step()
}

var (
	t0   = time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	good = Result{Time: t0, Size: 3 << 20, Key: "p/a.enc", OK: true, LastOK: t0}
	bad  = Result{Time: t0.Add(24 * time.Hour), Reason: "backup: put: status 403: AccessDenied", LastOK: t0,
		Kind: FailureRefused, Detail: "403 AccessDenied"}
	good2 = Result{Time: t0.Add(72 * time.Hour), Size: 5 << 20, Key: "p/b.enc", OK: true, LastOK: t0.Add(72 * time.Hour)}
)

func TestDailyWaitsForTheTimeAndDoesNotRunAtStart(t *testing.T) {
	k := newDailyKit(t, good)
	step, stop := k.start(k.daily())
	defer stop()
	if d := step(); d != 17*time.Hour { // 10:00 to 03:00 the next day
		t.Fatalf("first wait %v", d)
	}
	if k.run.count() != 0 {
		t.Fatal("it ran before the time")
	}
	if d := k.next(step); k.run.count() != 1 || d != 17*time.Hour {
		t.Fatalf("after the first run: wait %v, %d runs", d, k.run.count())
	}
}

func TestDailyNextWallTimeAcrossDST(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	d := &Daily{at: 3 * time.Hour, loc: loc}
	// The clocks go forward on 2027-03-28 at 03:00: that day is 23 hours long.
	before := time.Date(2027, 3, 27, 3, 30, 0, 0, loc)
	got := d.nextAfter(before)
	if h, _, _ := got.In(loc).Clock(); got.Day() != 28 || h != 4 { // 03:00 does not exist: the zone reads it as 04:00
		t.Fatalf("spring: %v", got)
	}
	again := d.nextAfter(got)
	if h, _, _ := again.In(loc).Clock(); again.Day() != 29 || h != 3 {
		t.Fatalf("the day after: %v", again)
	}
	// The clocks go back on 2026-10-25: that day is 25 hours long.
	fall := d.nextAfter(time.Date(2026, 10, 24, 12, 0, 0, 0, loc))
	if h, _, _ := fall.In(loc).Clock(); fall.Day() != 25 || h != 3 {
		t.Fatalf("autumn: %v", fall)
	}
	// Exactly at the time, the next run is tomorrow's.
	at := time.Date(2026, 11, 17, 3, 0, 0, 0, loc)
	if n := d.nextAfter(at); n.Sub(at) != 24*time.Hour {
		t.Fatalf("at the time: %v", n)
	}
}

func TestDailyTellsAFailureOnceAndTheRecoveryOnce(t *testing.T) {
	k := newDailyKit(t, bad, bad, bad, good2)
	step, stop := k.start(k.daily())
	defer stop()
	step()
	k.next(step) // failure 1: told
	if len(k.sent) != 2 {
		t.Fatalf("%d messages after the first failure", len(k.sent))
	}
	k.next(step)
	k.next(step) // failures 2 and 3: silent
	if len(k.sent) != 2 {
		t.Fatalf("%d messages after three failures", len(k.sent))
	}
	en, ru := k.sent[0].Text, k.sent[1].Text
	if k.sent[0].IdemKey != "backup:failed:"+stamp(t0)+":telegram:user:1" {
		t.Fatalf("key %q", k.sent[0].IdemKey)
	}
	for _, want := range []string{"The daily backup failed", "403 AccessDenied", "2026-11-17 03:00"} {
		if !strings.Contains(en, want) {
			t.Errorf("English alert lacks %q: %s", want, en)
		}
	}
	if !strings.Contains(ru, "не удалась") || !strings.Contains(ru, "403 AccessDenied") {
		t.Errorf("Russian alert: %s", ru)
	}
	for _, leak := range []string{"backup:", "https://", "status 403"} {
		if strings.Contains(en, leak) {
			t.Errorf("the alert holds %q: %s", leak, en)
		}
	}
	k.next(step) // recovery
	if len(k.sent) != 4 {
		t.Fatalf("%d messages after the recovery", len(k.sent))
	}
	if got := k.sent[2].Text; !strings.Contains(got, "5.0 MB") && !strings.Contains(got, "5 MB") || !strings.Contains(got, "2026-11-20") {
		t.Errorf("recovery text: %s", got)
	}
	k.next(step) // another success: silent
	if len(k.sent) != 4 {
		t.Fatalf("a plain success told: %d messages", len(k.sent))
	}
	if got := k.rec.Value(t, "ganglion.backup.runs", map[string]string{"outcome": "refused"}); got != 3 {
		t.Errorf("refused runs = %v", got)
	}
	if got := k.rec.Value(t, "ganglion.backup.runs", map[string]string{"outcome": "ok"}); got != 2 {
		t.Errorf("ok runs = %v", got)
	}
	if got := k.rec.Value(t, "ganglion.backup.last_success", nil); got != float64(good2.LastOK.Unix()) {
		t.Errorf("last success = %v", got)
	}
	if got := k.rec.Value(t, "ganglion.backup.last_size", nil); got != float64(good2.Size) {
		t.Errorf("last size = %v", got)
	}
}

func TestDailyDoesNotTellAgainAfterARestartInAFailureStreak(t *testing.T) {
	k := newDailyKit(t, bad, bad)
	if err := writeResult(k.dir, bad); err != nil {
		t.Fatal(err)
	}
	step, stop := k.start(k.daily())
	defer stop()
	step()
	k.next(step)
	if len(k.sent) != 0 {
		t.Fatalf("told again after a restart: %d messages", len(k.sent))
	}
}

func TestDailyFailureWithoutAnyGoodCopySaysNone(t *testing.T) {
	first := Result{Time: t0, Reason: "x", Kind: FailureOther}
	k := newDailyKit(t, first)
	step, stop := k.start(k.daily())
	defer stop()
	step()
	k.next(step)
	if len(k.sent) != 2 || !strings.Contains(k.sent[0].Text, "none yet") || !strings.Contains(k.sent[0].Text, "node's log") {
		t.Fatalf("%+v", k.sent)
	}
	if k.sent[0].IdemKey != "backup:failed:never:telegram:user:1" {
		t.Fatalf("key %q", k.sent[0].IdemKey)
	}
}

func TestDailyStopsWithItsContext(t *testing.T) {
	k := newDailyKit(t, good)
	step, stop := k.start(k.daily())
	step()
	stop()
	if k.run.count() != 0 {
		t.Fatal("it ran")
	}
}

func TestParseAt(t *testing.T) {
	if d, err := ParseAt("04:30"); err != nil || d != 4*time.Hour+30*time.Minute {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := ParseAt("24:00"); err == nil {
		t.Fatal("24:00 accepted")
	}
}

func TestEveryFailureHasAText(t *testing.T) {
	cat, err := texts.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Failure{FailureRefused, FailureNetwork, FailureChanged, FailureIntegrity, FailureOther, FailureNone} {
		for _, l := range []texts.Lang{texts.English, "ru", "uk"} {
			if cat.Text(l, ReasonKey(f), texts.Vars{"detail": "403"}) == "" {
				t.Errorf("%s %s: no text", f, l)
			}
		}
	}
}

func TestClassify(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		kind   Failure
		detail string
	}{
		"status":    {&StatusError{Status: 403, Code: "AccessDenied"}, FailureRefused, "403 AccessDenied"},
		"no code":   {&StatusError{Status: 500}, FailureRefused, "500"},
		"changed":   {errors.Join(ErrChanged, io.EOF), FailureChanged, ""},
		"integrity": {ErrIntegrity, FailureIntegrity, ""},
		"timeout":   {context.DeadlineExceeded, FailureNetwork, ""},
		"other":     {errors.New("boom /data/x"), FailureOther, ""},
	} {
		k, d := classify(tc.err)
		if k != tc.kind || d != tc.detail {
			t.Errorf("%s: %s %q", name, k, d)
		}
	}
}

func TestStatusLines(t *testing.T) {
	start := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	scope := func(h time.Duration) status.Scope { return status.Scope{Now: start.Add(h), Location: time.UTC} }
	one := func(t *testing.T, s *Status, sc status.Scope) status.Check {
		t.Helper()
		cs, err := s.Status(context.Background(), sc)
		if err != nil || len(cs) != 1 {
			t.Fatalf("%+v %v", cs, err)
		}
		return cs[0]
	}
	if c := one(t, NewStatus(false, t.TempDir(), start), scope(0)); c.State != status.Off || c.Line != texts.StatusBackupOff {
		t.Errorf("off: %+v", c)
	}
	dir := t.TempDir()
	s := NewStatus(true, dir, start)
	if c := one(t, s, scope(35*time.Hour)); c.State != status.OK || c.Line != texts.StatusBackupWaiting {
		t.Errorf("not yet: %+v", c)
	}
	if c := one(t, s, scope(37*time.Hour)); c.State != status.Warn || c.Line != texts.StatusBackupNone || c.Spans["for"] != 37*time.Hour {
		t.Errorf("never: %+v", c)
	}
	ok := Result{Time: start.Add(3 * time.Hour), Size: 2048, OK: true, LastOK: start.Add(3 * time.Hour)}
	if err := writeResult(dir, ok); err != nil {
		t.Fatal(err)
	}
	c := one(t, s, scope(10*time.Hour))
	if c.State != status.OK || c.Line != texts.StatusBackupOK || c.Vars["time"] != "03:00" || c.Vars["size"] != "2 KB" {
		t.Errorf("ok: %+v", c)
	}
	if c := one(t, s, scope(40*time.Hour)); c.State != status.Warn || c.Line != texts.StatusBackupStale || c.Spans["for"] != 37*time.Hour {
		t.Errorf("stale: %+v", c)
	}
	fail := Result{Time: start.Add(27 * time.Hour), Reason: "x", LastOK: ok.LastOK, Kind: FailureRefused, Detail: "403 AccessDenied"}
	if err := writeResult(dir, fail); err != nil {
		t.Fatal(err)
	}
	c = one(t, s, scope(28*time.Hour))
	if c.State != status.Fail || c.Reason != texts.BackupReasonRefused || c.Vars["detail"] != "403 AccessDenied" || !c.Since.Equal(fail.Time) {
		t.Errorf("failed: %+v", c)
	}
}

// syncPoll fires the poll and then once more: the second send is taken only
// when the component has finished what the first started.
func (k *dailyKit) syncPoll() {
	k.t.Helper()
	for range 2 {
		select {
		case k.tick <- time.Time{}:
		case <-time.After(5 * time.Second):
			k.t.Fatal("the component did not look for a request")
		}
	}
}

const testRequest = "0123456789ABCDEFGHJKMNPQRS"

func TestDailyServesARequestAndRecordsItsId(t *testing.T) {
	k := newDailyKit(t, good)
	step, stop := k.start(k.daily())
	defer stop()
	step()
	k.syncPoll()
	if k.run.count() != 0 {
		t.Fatal("it ran without a request")
	}
	if err := writeRequest(k.dir, testRequest); err != nil {
		t.Fatal(err)
	}
	k.syncPoll()
	if got := k.run.requests; len(got) != 1 || got[0] != testRequest {
		t.Fatalf("runs for %q", got)
	}
	if last, err := Last(k.dir); err != nil || last.Request != testRequest || !last.OK {
		t.Fatalf("recorded %+v, %v", last, err)
	}
	if _, err := os.Stat(RequestPath(k.dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the request is left: %v", err)
	}
	k.syncPoll()
	if k.run.count() != 1 {
		t.Fatal("it ran a request twice")
	}
	if d := k.next(step); k.run.count() != 2 || k.run.requests[1] != "" || d != 17*time.Hour {
		t.Fatalf("the daily run after it: %q, wait %v", k.run.requests, d)
	}
}

func TestDailyAnswersARequestOnlyOnce(t *testing.T) {
	k := newDailyKit(t, good)
	answered := good
	answered.Request = testRequest
	if err := writeResult(k.dir, answered); err != nil {
		t.Fatal(err)
	}
	if err := writeRequest(k.dir, testRequest); err != nil {
		t.Fatal(err)
	}
	step, stop := k.start(k.daily())
	defer stop()
	step()
	k.syncPoll()
	if k.run.count() != 0 {
		t.Fatal("a request the last result answers ran again")
	}
	if _, err := os.Stat(RequestPath(k.dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the answered request is left: %v", err)
	}
}

func TestDailyIgnoresAndRemovesARequestThatIsNotAnId(t *testing.T) {
	k := newDailyKit(t, good)
	step, stop := k.start(k.daily())
	defer stop()
	step()
	for _, bad := range []string{"", "short", testRequest + "X", "../../etc/passwd" + testRequest, "0123456789abcdefghjkmnpqrs"} {
		if err := os.WriteFile(RequestPath(k.dir), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		k.syncPoll()
		if _, err := os.Stat(RequestPath(k.dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%q: the request is left: %v", bad, err)
		}
	}
	if k.run.count() != 0 {
		t.Fatal("a bad request ran a backup")
	}
}

func TestDailyKeepsARequestWhileAnotherBackupRuns(t *testing.T) {
	k := newDailyKit(t, good)
	k.run.refuse = ErrBusy
	step, stop := k.start(k.daily())
	defer stop()
	step()
	if err := writeRequest(k.dir, testRequest); err != nil {
		t.Fatal(err)
	}
	k.syncPoll()
	if _, err := os.Stat(RequestPath(k.dir)); err != nil {
		t.Fatalf("the request was dropped: %v", err)
	}
	k.run.mu.Lock()
	k.run.refuse = nil
	k.run.mu.Unlock()
	k.syncPoll()
	if got := k.run.requests; len(got) != 1 || got[0] != testRequest {
		t.Fatalf("runs for %q", got)
	}
}

// askKit is Ask with an injected wait: the answers come from the test.
type askKit struct {
	dir   string
	polls int
	// onPoll runs at the n-th wait for an answer, with the request's id.
	onPoll  func(n int, id string)
	timeout bool
}

func (a *askKit) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	switch {
	case d == AnswerWait && a.timeout:
		ch <- time.Time{}
	case d == AnswerPoll && !a.timeout:
		a.polls++
		data, _ := os.ReadFile(RequestPath(a.dir))
		a.onPoll(a.polls, string(data))
		ch <- time.Time{}
	}
	return ch
}

func TestAskWaitsForTheResultWithItsId(t *testing.T) {
	dir := t.TempDir()
	a := &askKit{dir: dir}
	a.onPoll = func(n int, id string) {
		switch n {
		case 1: // an older result and one for another request do not answer
			_ = writeResult(dir, Result{OK: true, Key: "old", Request: "someone else"})
		case 3:
			if !validRequestID(id) {
				t.Errorf("the request holds %q", id)
			}
			_ = writeResult(dir, Result{OK: true, Key: "p/a.enc", Size: 9, Request: id})
		}
	}
	res, err := Ask(context.Background(), dir, a.after)
	if err != nil || res.Key != "p/a.enc" || a.polls != 3 {
		t.Fatalf("%+v, %v after %d polls", res, err, a.polls)
	}
	if info, err := os.Stat(RequestPath(dir)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the request file: %v, %v", info, err)
	}
}

func TestAskReturnsAFailureWithItsReason(t *testing.T) {
	dir := t.TempDir()
	a := &askKit{dir: dir, onPoll: func(_ int, id string) {
		_ = writeResult(dir, Result{Reason: "backup: put: status 403", Kind: FailureRefused, Request: id})
	}}
	res, err := Ask(context.Background(), dir, a.after)
	if err == nil || err.Error() != "backup: put: status 403" || res.OK {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestAskGivesUpAndWithdrawsTheRequest(t *testing.T) {
	dir := t.TempDir()
	a := &askKit{dir: dir, timeout: true}
	if _, err := Ask(context.Background(), dir, a.after); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(RequestPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the request is left: %v", err)
	}
}

func TestAskStopsWithItsContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := func(time.Duration) <-chan time.Time { return nil }
	if _, err := Ask(ctx, dir, never); !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(RequestPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the request is left: %v", err)
	}
}

func TestAskRefusesWhileAnotherRequestWaits(t *testing.T) {
	dir := t.TempDir()
	if err := writeRequest(dir, testRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := Ask(context.Background(), dir, func(time.Duration) <-chan time.Time { return nil }); !errors.Is(err, ErrPending) {
		t.Fatalf("error %v", err)
	}
	if id, ok := readRequest(dir); !ok || id != testRequest {
		t.Fatalf("the waiting request changed: %q", id)
	}
}
