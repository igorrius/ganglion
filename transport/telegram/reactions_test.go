package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

var reactT0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// reactEnv is a Reactor on the fake Bot API with a clock the test moves.
type reactEnv struct {
	fake *telegramtest.Server
	r    *Reactor
	now  time.Time
}

func newReactEnv(t *testing.T) *reactEnv {
	t.Helper()
	e := &reactEnv{fake: telegramtest.NewServer(t, testToken), now: reactT0}
	cfg := DefaultReactorConfig()
	cfg.Now = func() time.Time { return e.now }
	e.r = NewReactor(testToken, e.fake.URL(), cfg, slog.New(slog.DiscardHandler))
	return e
}

// at moves the clock to sec seconds from the start and takes a step.
func (e *reactEnv) at(sec float64) {
	e.now = reactT0.Add(time.Duration(sec * float64(time.Second)))
	e.r.step(context.Background(), e.now)
}

func (e *reactEnv) report(ref string, st event.Stage) {
	e.r.Report(event.Status{Transport: Name, Conv: "telegram:42", Ref: ref, Stage: st})
}

// shown lists the reactions set so far.
func (e *reactEnv) shown() []string {
	out := []string{}
	for _, r := range e.fake.Reactions() {
		out = append(out, r.Emoji)
	}
	return out
}

func wantShown(t *testing.T, e *reactEnv, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := e.shown(); !slices.Equal(got, want) {
		t.Fatalf("reactions %q, want %q", got, want)
	}
}

func TestQuickAnswerShowsNothing(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageThinking)
	e.at(5)
	e.report("5", event.StageDone)
	e.at(6)
	e.at(9)
	wantShown(t, e)
	if n := e.fake.Calls("setMessageReaction"); n != 0 {
		t.Fatalf("%d API calls", n)
	}
}

func TestSlowWorkShowsAfterTheThresholdAndComesOff(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageThinking)
	e.at(7.9)
	wantShown(t, e)
	e.at(8)
	wantShown(t, e, emojiThinking)
	e.at(10)
	e.report("5", event.StageDone)
	e.at(10.5)
	wantShown(t, e, emojiThinking)
	e.at(10.8)
	wantShown(t, e, emojiThinking, "")
	e.at(11)
	e.at(30)
	wantShown(t, e, emojiThinking, "")
}

func TestToolStaysToTheEnd(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageTool)
	e.at(8)
	e.report("5", event.StageThinking)
	e.at(20)
	wantShown(t, e, emojiTool)
}

func TestDwellHoldsChanges(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageThinking)
	e.at(8)
	e.report("5", event.StageTool)
	e.at(9)
	e.at(10.9)
	wantShown(t, e, emojiThinking)
	e.at(11)
	wantShown(t, e, emojiThinking, emojiTool)
}

func TestAddedShowsAtOnce(t *testing.T) {
	e := newReactEnv(t)
	e.report("6", event.StageQueued)
	e.report("6", event.StageAdded)
	e.at(1)
	wantShown(t, e, emojiAdded)
}

func TestAddedIgnoresTheDwell(t *testing.T) {
	e := newReactEnv(t)
	e.report("6", event.StageQueued)
	e.at(8)
	e.report("6", event.StageAdded)
	e.at(8.2)
	wantShown(t, e, emojiQueued, emojiAdded)
}

func TestQueuedMessageShowsSeen(t *testing.T) {
	e := newReactEnv(t)
	e.report("6", event.StageQueued)
	e.at(8)
	wantShown(t, e, emojiQueued)
	e.report("6", event.StageThinking)
	e.at(10)
	wantShown(t, e, emojiQueued)
	e.at(11)
	wantShown(t, e, emojiQueued, emojiThinking)
}

func TestGroupsShowNothing(t *testing.T) {
	e := newReactEnv(t)
	e.r.Report(event.Status{Transport: Name, Conv: "telegram:-100", Ref: "5", Stage: event.StageAdded})
	e.at(10)
	if n := e.fake.Calls("setMessageReaction"); n != 0 {
		t.Fatalf("%d API calls", n)
	}
}

func TestOtherTransportsAndOffShowNothing(t *testing.T) {
	e := newReactEnv(t)
	e.r.Report(event.Status{Transport: "mqtt", Conv: "telegram:42", Ref: "5", Stage: event.StageAdded})
	e.report("not a number", event.StageAdded)
	e.at(10)
	wantShown(t, e)
	cfg := DefaultReactorConfig()
	cfg.After, cfg.Now = 0, func() time.Time { return e.now }
	off := NewReactor(testToken, e.fake.URL(), cfg, slog.New(slog.DiscardHandler))
	off.Report(event.Status{Transport: Name, Conv: "telegram:42", Ref: "5", Stage: event.StageAdded})
	off.step(context.Background(), e.now)
	wantShown(t, e)
}

// Review focus 4.
func TestRefusedReactionDoesNotRepeat(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newReactEnv(t)
	e.fake.FailNext("setMessageReaction", 400, 0)
	e.report("5", event.StageThinking)
	e.report("6", event.StageDone)
	e.at(8)
	calls := e.fake.Calls("setMessageReaction")
	e.at(12)
	e.report("5", event.StageTool)
	e.at(20)
	if n := e.fake.Calls("setMessageReaction"); n != calls {
		t.Fatalf("tried again: %d calls, then %d", calls, n)
	}
	if got := rec.Value(t, "ganglion.telegram.reactions", map[string]string{"outcome": "refused"}); got != 1 {
		t.Fatalf("refused = %v", got)
	}
	e.report("7", event.StageThinking)
	e.at(28)
	wantShown(t, e, emojiThinking)
}

// Review focus 4.
func Test429PausesAllReactions(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newReactEnv(t)
	e.fake.FailNext("setMessageReaction", 429, 30)
	e.report("5", event.StageThinking)
	e.at(8)
	calls := e.fake.Calls("setMessageReaction")
	e.report("6", event.StageAdded)
	e.at(9)
	e.report("5", event.StageDone)
	e.at(37)
	if n := e.fake.Calls("setMessageReaction"); n != calls {
		t.Fatalf("called during the pause: %d, then %d", calls, n)
	}
	e.at(38.1)
	// Nothing was shown on 5, so its Done needs no call; 6 still gets its mark.
	wantShown(t, e, emojiAdded)
	if got := rec.Value(t, "ganglion.telegram.reactions", map[string]string{"outcome": "rate_limited"}); got != 1 {
		t.Fatalf("rate limited = %v", got)
	}
}

func TestDoneClearsAfterAPause(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageThinking)
	e.at(8)
	e.fake.FailNext("setMessageReaction", 429, 30)
	e.report("5", event.StageDone)
	e.at(9)
	e.at(39.1)
	wantShown(t, e, emojiThinking, "")
}

// A message whose turn never reported its end loses its reaction anyway, and
// the entry goes.
// Review: a change that fails does not leave the old reaction for good: the end
// of the turn still takes it off, once.
func TestFailedChangeStillComesOffAtTheEnd(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageThinking)
	e.at(8)
	e.fake.FailNext("setMessageReaction", 502, 0)
	e.report("5", event.StageTool)
	e.at(11)
	e.report("5", event.StageDone)
	e.at(12)
	e.at(13)
	wantShown(t, e, emojiThinking, "")
}

func TestOldEntryIsClearedAndDropped(t *testing.T) {
	e := newReactEnv(t)
	e.report("5", event.StageQueued)
	e.at(8)
	e.at(reactionMaxAge.Seconds() + 1)
	wantShown(t, e, emojiQueued, "")
	e.r.mu.Lock()
	n := len(e.r.msgs)
	e.r.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d entries left", n)
	}
}

func TestStrangerGetsNoReaction(t *testing.T) {
	e := newReactEnv(t)
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	list, err := access.New(ctx, db, access.WithAdmin("telegram:user:42"))
	if err != nil {
		t.Fatal(err)
	}
	var put int
	sink := func(context.Context, event.Event) error { put++; return nil }
	var _ transport.Sink = sink
	filtered := list.Filter(e.r.Noting(sink), slog.New(slog.DiscardHandler))
	msg := func(from, ref string) event.Event {
		p, _ := json.Marshal(event.Message{Text: "hi", From: from, Ref: ref})
		return event.Event{Source: Name, Conv: "telegram:42", Kind: event.KindMessage, Payload: p}
	}
	if err := filtered(ctx, msg("telegram:user:666", "5")); err == nil {
		t.Fatal("a stranger was let in")
	}
	e.at(10)
	wantShown(t, e)
	if err := filtered(ctx, msg("telegram:user:42", "6")); err != nil {
		t.Fatal(err)
	}
	e.at(19)
	wantShown(t, e, emojiQueued)
	if put != 1 {
		t.Fatalf("%d put", put)
	}
}

func TestResumedTurnShowsTyping(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	tr.typingEvery = 20 * time.Millisecond
	stop := tr.TypingHook(context.Background(), event.Event{Source: Name, Kind: event.KindDecision, Conv: "telegram:42"})
	deadline := time.Now().Add(2 * time.Second)
	for len(fake.ChatActions()) == 0 && time.Now().Before(deadline) {
		<-time.After(5 * time.Millisecond)
	}
	stop()
	if len(fake.ChatActions()) == 0 {
		t.Fatal("no typing on a resumed turn")
	}
}
