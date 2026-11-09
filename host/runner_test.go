package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// testLimits are short enough for tests and leave the clock to the gate.
var testLimits = Limits{Wait: time.Second, Max: time.Minute, Parallel: 2, Head: 16, Tail: 16}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(t.Context(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// start runs r until the test ends, and waits for Run to return.
func start(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
}

func newRunner(t *testing.T, s Starter, box *inbox, g gate) *Runner {
	t.Helper()
	r, err := NewRunner(t.Context(), openDB(t), s, box.put, testLimits, WithRunnerClock(time.Now, g.after))
	if err != nil {
		t.Fatal(err)
	}
	start(t, r)
	return r
}

func req(key string, argv ...string) Request {
	return Request{Key: key, Conv: "telegram:1", Transport: "telegram", Principal: "telegram:user:1", Argv: argv, Dir: "/"}
}

// drive opens the announcing pass until box holds n events, for at most 10 s.
func drive(t *testing.T, g gate, box *inbox, n int) []event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for len(box.events()) < n {
		select {
		case g.pass <- time.Now():
		case <-ctx.Done():
			t.Fatalf("%d events after 10 s, want %d: %+v", len(box.events()), n, box.events())
		}
	}
	return box.events()
}

// openWait ends the short wait of the next call that waits.
func openWait(g gate) <-chan struct{} {
	opened := make(chan struct{})
	go func() { g.wait <- time.Now(); close(opened) }()
	return opened
}

func TestExecWaitsForAQuickCommand(t *testing.T) {
	r := newRunner(t, &shell{}, &inbox{}, newGate())
	a, err := r.Exec(t.Context(), req("t:1", "echo", "hello"))
	if err != nil || !a.Done || a.Job.State != JobDone {
		t.Fatalf("answer = %+v, %v", a, err)
	}
	if !strings.HasPrefix(a.Job.Text, "exit 0") || !strings.Contains(a.Job.Text, "hello") {
		t.Fatalf("text = %q", a.Job.Text)
	}
}

func TestACommandRunsOncePerKey(t *testing.T) {
	sh := &shell{}
	r := newRunner(t, sh, &inbox{}, newGate())
	var texts []string
	for range 2 {
		a, err := r.Exec(t.Context(), req("t:1", "echo", "x"))
		if err != nil || !a.Done {
			t.Fatalf("answer = %+v, %v", a, err)
		}
		texts = append(texts, a.Job.Text)
	}
	if sh.started() != 1 {
		t.Fatalf("started %d times, want once", sh.started())
	}
	if texts[0] != texts[1] {
		t.Fatalf("the rerun answered %q, the run %q", texts[1], texts[0])
	}
}

// A command gets no input: stdin is /dev/null, so a program that reads it
// sees its end at once (find -ok's question reads EOF and runs nothing).
func TestACommandReadsNoInput(t *testing.T) {
	r := newRunner(t, &shell{}, &inbox{}, newGate())
	q := req("t:in", "cat")
	q.Timeout = 5 * time.Second
	a, err := r.Exec(t.Context(), q)
	if err != nil || !a.Done || !strings.HasPrefix(a.Job.Text, "exit 0") || !strings.Contains(a.Job.Text, "; 0 bytes of output") {
		t.Fatalf("answer = %+v, %v", a, err)
	}
}

func TestPastTheWaitItGoesOnAndIsAnnounced(t *testing.T) {
	box, g := &inbox{}, newGate()
	r := newRunner(t, &shell{}, box, g)
	opened := openWait(g)
	a, err := r.Exec(t.Context(), req("t:2", "sleep", "0.3;", "echo", "late"))
	<-opened
	if err != nil || a.Done || a.Job.State != JobRunning || !a.Job.Background {
		t.Fatalf("answer = %+v, %v; want running in the background", a, err)
	}
	evs := drive(t, g, box, 1)
	if len(evs) != 1 || evs[0].Kind != event.KindCommandDone || evs[0].Conv != "telegram:1" ||
		evs[0].Source != "telegram" || evs[0].IdemKey != "command:t:2:done" {
		t.Fatalf("events = %+v", evs)
	}
	var p event.CommandDone
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p.Key != "t:2" {
		t.Fatalf("payload = %s, %v", evs[0].Payload, err)
	}
	j, err := r.Get(t.Context(), "t:2")
	if err != nil || j.State != JobDone || !strings.Contains(j.Text, "late") {
		t.Fatalf("job = %+v, %v", j, err)
	}
	g.pass <- time.Now() // one more pass: an announced job is not announced again
	g.pass <- time.Now()
	if n := len(box.events()); n != 1 {
		t.Fatalf("announced %d times", n)
	}
}

func TestAQuickCommandIsNeverAnnounced(t *testing.T) {
	box, g := &inbox{}, newGate()
	r := newRunner(t, &shell{}, box, g)
	if _, err := r.Exec(t.Context(), req("t:3", "true")); err != nil {
		t.Fatal(err)
	}
	g.pass <- time.Now()
	g.pass <- time.Now()
	if n := len(box.events()); n != 0 {
		t.Fatalf("a command answered in its call was announced %d times", n)
	}
}

// A rerun of a key whose command still runs learns it runs in the
// background; the first call then hears it the same way, and its end comes
// once, as the event.
func TestARerunWhileItRunsSendsBothCallsToTheBackground(t *testing.T) {
	box, g := &inbox{}, newGate()
	sh := &shell{}
	r := newRunner(t, sh, box, g)
	release := filepath.Join(t.TempDir(), "go")
	first := make(chan Answer, 1)
	go func() {
		a, err := r.Exec(context.Background(), req("t:8", "while", "[", "!", "-e", release, "];", "do", "sleep", "0.01;", "done;", "echo", "out"))
		if err != nil {
			t.Error(err)
		}
		first <- a
	}()
	for {
		if j, err := r.Get(t.Context(), "t:8"); err == nil && j.State == JobRunning {
			break
		}
		select {
		case a := <-first:
			t.Fatalf("the first call ended before its command could: %+v", a)
		case <-t.Context().Done():
			t.Fatal("no row")
		default:
		}
	}
	again, err := r.Exec(t.Context(), req("t:8", "ignored"))
	if err != nil || again.Done || !again.Job.Background {
		t.Fatalf("rerun = %+v, %v; want running in the background", again, err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := <-first
	if a.Done {
		t.Fatalf("first call = %+v; its end is the event's to tell", a)
	}
	evs := drive(t, g, box, 1)
	if len(evs) != 1 || sh.started() != 1 {
		t.Fatalf("events = %+v, started %d", evs, sh.started())
	}
}

func TestTheTimeLimitKillsTheGroup(t *testing.T) {
	r := newRunner(t, &shell{}, &inbox{}, newGate())
	q := req("t:4", "sleep", "60", "&", "sleep", "60;", "wait")
	q.Timeout = 200 * time.Millisecond
	a, err := r.Exec(t.Context(), q)
	if err != nil || !a.Done || !strings.HasPrefix(a.Job.Text, "stopped after 200ms: past its time limit") {
		t.Fatalf("answer = %+v, %v", a, err)
	}
}

func TestAProgramThatDoesNotStartIsNotRun(t *testing.T) {
	r := newRunner(t, offStarter{}, &inbox{}, newGate())
	a, err := r.Exec(t.Context(), req("t:off", "true"))
	if err != nil || !a.Done || a.Job.Text != "not run: sandbox: off" {
		t.Fatalf("answer = %+v, %v", a, err)
	}
}

func TestOutputKeepsTheHeadAndTheTailAndIsRedacted(t *testing.T) {
	lim := Limits{Wait: time.Minute, Max: time.Minute, Parallel: 1, Head: 8, Tail: 8}
	r, err := NewRunner(t.Context(), openDB(t), &shell{}, (&inbox{}).put, lim,
		WithRedact(func(s string) string { return strings.ReplaceAll(s, "SECRET", "[redacted]") }))
	if err != nil {
		t.Fatal(err)
	}
	start(t, r)
	a, err := r.Exec(t.Context(), req("t:5", "printf", "'SECRET-start-...-centre-...-end-SECRET'"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Job.Text, "SECRET") || !strings.Contains(a.Job.Text, "left out") || strings.Contains(a.Job.Text, "centre") ||
		!strings.Contains(a.Job.Text, "; 38 bytes of output, the middle left out") {
		t.Fatalf("text = %q", a.Job.Text)
	}
}

func TestARestartInterruptsAndAnnouncesBackgroundJobs(t *testing.T) {
	rec := telemetrytest.Install(t)
	db := openDB(t)
	if _, err := NewRunner(t.Context(), db, &shell{}, (&inbox{}).put, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	// Two rows a killed node left: one whose call was still waiting, one in the background.
	for _, k := range []struct {
		key string
		bg  int
	}{{"t:6", 0}, {"t:7", 1}} {
		if _, err := db.Exec(`INSERT INTO host_jobs (key, conv, transport, principal, depth, argv, dir, state, background, started_ms)
			VALUES (?, 'telegram:1', 'telegram', 'p', 0, '["sleep","9"]', '/', 'running', ?, 1)`, k.key, k.bg); err != nil {
			t.Fatal(err)
		}
	}
	box, g, sh := &inbox{}, newGate(), &shell{}
	r, err := NewRunner(t.Context(), db, sh, box.put, DefaultLimits(), WithRunnerClock(time.Now, g.after))
	if err != nil {
		t.Fatal(err)
	}
	start(t, r)
	if evs := drive(t, g, box, 1); len(evs) != 1 || !strings.Contains(string(evs[0].Payload), "t:7") {
		t.Fatalf("events = %+v, want only the background job", evs)
	}
	a, err := r.Exec(t.Context(), req("t:6", "sleep", "9"))
	if err != nil || !a.Done || a.Job.State != JobInterrupted || !strings.Contains(a.Job.Text, "restart") {
		t.Fatalf("rerun of an interrupted call = %+v, %v; want its interruption, not a new run", a, err)
	}
	if sh.started() != 0 {
		t.Fatalf("an interrupted command ran again")
	}
	if got := rec.Value(t, "ganglion.host.commands", map[string]string{"outcome": "interrupted"}); got != 2 {
		t.Fatalf("interrupted counted %v, want 2", got)
	}
}

// Parallel 2: two long commands hold the slots; a third that comes now goes
// to the background without starting, and starts when one ends.
func TestAThirdCommandWaitsForASlot(t *testing.T) {
	box, g, sh := &inbox{}, newGate(), &shell{}
	r := newRunner(t, sh, box, g)
	release := filepath.Join(t.TempDir(), "go")
	hold := []string{"while", "[", "!", "-e", release, "];", "do", "sleep", "0.01;", "done"}
	for _, key := range []string{"t:a", "t:b"} {
		opened := openWait(g)
		a, err := r.Exec(t.Context(), req(key, hold...))
		<-opened
		if err != nil || a.Done {
			t.Fatalf("%s = %+v, %v; want running", key, a, err)
		}
	}
	for sh.started() < 2 { // both hold their slot once their commands are made
		select {
		case <-t.Context().Done():
			t.Fatal("the two commands did not start")
		default:
		}
	}
	opened := openWait(g)
	a, err := r.Exec(t.Context(), req("t:c", "echo", "third"))
	<-opened
	if err != nil || a.Done || a.Job.State != JobRunning {
		t.Fatalf("third = %+v, %v; want waiting in the background", a, err)
	}
	if n := sh.started(); n != 2 {
		t.Fatalf("started %d commands with two slots held", n)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	evs := drive(t, g, box, 3)
	keys := map[string]bool{}
	for _, e := range evs {
		var p event.CommandDone
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		keys[p.Key] = true
	}
	if len(keys) != 3 || !keys["t:c"] {
		t.Fatalf("announced %v", keys)
	}
	j, err := r.Get(t.Context(), "t:c")
	if err != nil || j.State != JobDone || !strings.Contains(j.Text, "third") || sh.started() != 3 {
		t.Fatalf("third = %+v, %v; started %d", j, err, sh.started())
	}
}

func TestRunningListsTheKeysStillRunning(t *testing.T) {
	g := newGate()
	r := newRunner(t, &shell{}, &inbox{}, g)
	release := filepath.Join(t.TempDir(), "go")
	opened := openWait(g)
	if _, err := r.Exec(t.Context(), req("t:long", "while", "[", "!", "-e", release, "];", "do", "sleep", "0.01;", "done")); err != nil {
		t.Fatal(err)
	}
	<-opened
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	if _, err := r.Exec(t.Context(), req("t:quick", "true")); err != nil {
		t.Fatal(err)
	}
	got, err := r.Running(t.Context(), []string{"t:long", "t:quick", "t:none"})
	if err != nil || len(got) != 1 || got[0] != "t:long" {
		t.Fatalf("Running = %v, %v", got, err)
	}
	if _, err := r.Get(t.Context(), "t:none"); err != ErrNoJob {
		t.Fatalf("Get of an unknown key: %v, want ErrNoJob", err)
	}
}

// Run returns only after the commands it started ended: the end of its
// context kills them, and their rows are stored before it returns.
func TestRunWaitsForItsCommands(t *testing.T) {
	db := openDB(t)
	g := newGate()
	r, err := NewRunner(t.Context(), db, &shell{}, (&inbox{}).put, testLimits, WithRunnerClock(time.Now, g.after))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	began := filepath.Join(t.TempDir(), "began")
	opened := openWait(g)
	if _, err := r.Exec(t.Context(), req("t:9", "touch", began+";", "sleep", "60")); err != nil {
		t.Fatal(err)
	}
	<-opened
	for { // the command runs before the node stops
		if _, err := os.Stat(began); err == nil {
			break
		}
		select {
		case <-t.Context().Done():
			t.Fatal("the command did not start")
		default:
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	j, err := r.Get(t.Context(), "t:9")
	if err != nil || j.State != JobInterrupted {
		t.Fatalf("job after the stop = %+v, %v; want interrupted", j, err)
	}
	if _, err := r.Exec(t.Context(), req("t:10", "true")); err == nil {
		t.Fatal("a stopped runner took a command")
	}
}

func TestTheRunnerCountsAndTimesCommands(t *testing.T) {
	rec := telemetrytest.Install(t)
	r := newRunner(t, &shell{}, &inbox{}, newGate())
	for i, argv := range [][]string{{"true"}, {"false"}} {
		if _, err := r.Exec(t.Context(), req("t:m"+string(rune('0'+i)), argv...)); err != nil {
			t.Fatal(err)
		}
	}
	for outcome, want := range map[string]float64{"ok": 1, "failed": 1} {
		if got := rec.Value(t, "ganglion.host.commands", map[string]string{"outcome": outcome}); got != want {
			t.Errorf("%s: %v, want %v", outcome, got, want)
		}
	}
	if got := rec.Value(t, "ganglion.host.command.duration", nil); got != 2 {
		t.Errorf("duration recorded %v times, want 2", got)
	}
	if got := rec.Value(t, "ganglion.host.commands.running", nil); got != 0 {
		t.Errorf("running = %v, want 0", got)
	}
}

func TestNewRunnerRefusesLimitsWithoutASlot(t *testing.T) {
	lim := testLimits
	lim.Parallel = 0
	if _, err := NewRunner(t.Context(), openDB(t), &shell{}, (&inbox{}).put, lim); err == nil {
		t.Fatal("no slot accepted")
	}
}

// errSandboxOff is what an off sandbox answers.
var errSandboxOff = errors.New("sandbox: off")

// offStarter is a sandbox that is off.
type offStarter struct{}

func (offStarter) Command(context.Context, []string, string, ...string) (*exec.Cmd, error) {
	return nil, errSandboxOff
}

// A rerun of a key whose command ended in the background is not told its
// end: the event told it, and each end is told once (Review Focus 5).
func TestARerunOfAnAnnouncedJobIsNotDone(t *testing.T) {
	box, g, sh := &inbox{}, newGate(), &shell{}
	r := newRunner(t, sh, box, g)
	opened := openWait(g)
	if a, err := r.Exec(t.Context(), req("t:11", "sleep", "0.3;", "echo", "late")); err != nil || a.Done {
		t.Fatalf("answer = %+v, %v; want running", a, err)
	}
	<-opened
	drive(t, g, box, 1)
	a, err := r.Exec(t.Context(), req("t:11", "ignored"))
	if err != nil || a.Done || a.Job.State != JobDone || sh.started() != 1 {
		t.Fatalf("rerun = %+v, %v, started %d; want not done, from the stored row", a, err, sh.started())
	}
}

// secret is a token the tests' redactor knows, 46 characters long.
const secret = "tok-0123456789abcdefghijklmnopqrstuvwxyzABCDEF"

// hideSecret is the tests' redactor.
func hideSecret(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") }

// noPartOf fails when any 6 characters of secret appear in text.
func noPartOf(t *testing.T, text string) {
	t.Helper()
	for i := 0; i+6 <= len(secret); i++ {
		if strings.Contains(text, secret[i:i+6]) {
			t.Fatalf("%q of the secret reached the text: %q", secret[i:i+6], text)
		}
	}
}

// straddling is output with a secret across the end of the kept head and
// another across the start of the kept tail.
func straddling(head, tail int) string {
	return strings.Repeat("x", head-20) + secret + strings.Repeat("y", 1000) + secret + strings.Repeat("z", tail-26)
}

// A secret the cut goes through is hidden whole before the cut, so not even
// a part of it reaches the model.
func TestASecretAcrossTheCutIsHiddenWhole(t *testing.T) {
	lim := Limits{Wait: time.Minute, Max: time.Minute, Parallel: 1, Head: 64, Tail: 64}
	r, err := NewRunner(t.Context(), openDB(t), &shell{}, (&inbox{}).put, lim, WithRedact(hideSecret))
	if err != nil {
		t.Fatal(err)
	}
	start(t, r)
	a, err := r.Exec(t.Context(), req("t:12", "printf", "%s", straddling(64, 64)))
	if err != nil || !strings.Contains(a.Job.Text, "the middle left out") {
		t.Fatalf("answer = %+v, %v", a, err)
	}
	noPartOf(t, a.Job.Text)
}
