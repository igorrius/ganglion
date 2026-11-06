package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/texts"
)

// fakeLanguages is a person's language kept in a map.
type fakeLanguages struct {
	seen map[string]string
	lang map[string]texts.Lang
	err  error
	// notified holds the event that told a person, by person and language.
	notified map[string]string
}

func (f *fakeLanguages) Seen(_ context.Context, principal, code string) error {
	if f.seen == nil {
		f.seen = map[string]string{}
	}
	f.seen[principal] = code
	return f.err
}

func (f *fakeLanguages) NoticeFor(_ context.Context, principal string, l texts.Lang, event string) (bool, error) {
	if f.notified == nil {
		f.notified = map[string]string{}
	}
	key := principal + "/" + string(l)
	if prev, ok := f.notified[key]; ok {
		return prev == event, nil
	}
	f.notified[key] = event
	return true, nil
}

func (f *fakeLanguages) Resolve(_ context.Context, principal string) (texts.Lang, error) {
	if f.err != nil {
		return texts.English, f.err
	}
	if l, ok := f.lang[principal]; ok {
		return l, nil
	}
	return texts.English, nil
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func withLang(ev event.Event, code string) event.Event {
	var m event.Message
	if err := json.Unmarshal(ev.Payload, &m); err != nil {
		panic(err)
	}
	m.Lang = code
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	ev.Payload = raw
	return ev
}

func TestMessageRecordsTheChannelsLanguage(t *testing.T) {
	langs := &fakeLanguages{}
	a := &Agent{d: Deps{Languages: langs}, log: discardLog()}
	tn := &turn{event: withLang(message("telegram:user:1", "Igor", "hi"), "uk")}
	if !payloads[tn.event.Kind](tn) {
		t.Fatal("not decoded")
	}
	a.noteLanguage(context.Background(), tn)
	if got := langs.seen["telegram:user:1"]; got != "uk" {
		t.Fatalf("seen %q", got)
	}
}

func TestDecisionRecordsThePressersLanguage(t *testing.T) {
	langs := &fakeLanguages{}
	a := &Agent{d: Deps{Languages: langs}, log: discardLog()}
	raw, _ := json.Marshal(event.Decision{Button: "b", From: "telegram:user:2", Lang: "ru"})
	tn := &turn{event: event.Event{Kind: event.KindDecision, Payload: raw}}
	if !payloads[tn.event.Kind](tn) {
		t.Fatal("not decoded")
	}
	a.noteLanguage(context.Background(), tn)
	if got := langs.seen["telegram:user:2"]; got != "ru" {
		t.Fatalf("seen %q", got)
	}
}

func TestTurnResolvesLanguageOfItsPerson(t *testing.T) {
	langs := &fakeLanguages{lang: map[string]texts.Lang{"a": "uk", "owner": "pl"}}
	a := &Agent{d: Deps{Languages: langs}, log: discardLog()}
	tn := &turn{msg: event.Message{From: "a"}}
	if got := a.language(context.Background(), tn); got != "uk" {
		t.Fatalf("person: %s", got)
	}
	// A scheduled turn learns its job's owner in a stage.
	tn.msg.From = "owner"
	if got := a.language(context.Background(), tn); got != "pl" {
		t.Fatalf("job's owner: %s", got)
	}
}

func TestLanguageFailureGivesEnglish(t *testing.T) {
	a := &Agent{d: Deps{Languages: &fakeLanguages{err: errors.New("disk")}}, log: discardLog()}
	if got := a.language(context.Background(), &turn{msg: event.Message{From: "a"}}); got != texts.English {
		t.Fatalf("%s", got)
	}
}

func TestLanguageFailureLosesNoMessage(_ *testing.T) {
	langs := &fakeLanguages{err: errors.New("disk")}
	a := &Agent{d: Deps{Languages: langs}, log: discardLog()}
	tn := &turn{event: withLang(message("a", "A", "hi"), "uk")}
	payloads[tn.event.Kind](tn)
	a.noteLanguage(context.Background(), tn) // no panic, no error to retry the event over
}

func TestHandleRecordsTheLanguage(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	langs := &fakeLanguages{}
	e.deps.Languages = langs
	e.a = New(overDefaults(Config{}), e.deps)
	e.turn(t, withLang(message("telegram:user:1", "Igor", "/new"), "uk"))
	if got := langs.seen["telegram:user:1"]; got != "uk" {
		t.Fatalf("seen %q", got)
	}
}

// inLanguage is an agent whose principal "uk" reads Ukrainian and "en" English.
func inLanguage(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	e.deps.Languages = &fakeLanguages{lang: map[string]texts.Lang{"uk": "uk"}}
	e.a = New(overDefaults(Config{}), e.deps)
	return e
}

func TestRepliesFollowTheLanguage(t *testing.T) {
	for _, tc := range []struct {
		cmd string
		en  string
		uk  string
	}{
		{"/new", "New session started.", "Нову розмову розпочато."},
		{"/sessions", "No earlier sessions.", "Попередніх розмов немає."},
		{"/resume 9", "No such session; see /sessions.", "Такої розмови немає; дивіться /sessions."},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := inLanguage(t).turn(t, message("en", "A", tc.cmd)); got[0].Text != tc.en {
				t.Errorf("en: %q", got[0].Text)
			}
			if got := inLanguage(t).turn(t, message("uk", "A", tc.cmd)); got[0].Text != tc.uk {
				t.Errorf("uk: %q", got[0].Text)
			}
		})
	}
}

func TestEveryKeyInTheInventoryHasASlot(t *testing.T) {
	// The texts a person meets on a turn with no model or a press: each is a key.
	for _, k := range []texts.Key{
		texts.ReplyNoModel, texts.ReplyStepLimit, texts.ReplyExpired, texts.ReplyDailyLimit, texts.ReplyGiveUp,
		texts.ReplyNewSession, texts.ReplyNoSessions, texts.ReplyNoSuchResume, texts.ReplyResumed, texts.ReplySessionRow,
		texts.ReplyNoTasks, texts.ReplyNotAvailable,
		texts.LineRunning, texts.LineApproved, texts.LineApprovedFailed, texts.LineDeclined, texts.LineUsed, texts.LineExpired,
	} {
		if texts.EnglishText(k) == "" {
			t.Errorf("%s has no English text", k)
		}
	}
}

func TestSessionRowUsesNoPlural(t *testing.T) {
	if s := texts.EnglishText(texts.ReplySessionRow); !strings.Contains(s, "turns: {turns}") {
		t.Errorf("a count must not change a noun's form: %q", s)
	}
}

func TestGiveUpFollowsTheLanguage(t *testing.T) {
	e := inLanguage(t)
	out := e.a.GiveUp(message("uk", "A", "hi"), 3)
	if len(out) == 0 || out[0].Text != "Вибачте, з цим повідомленням щось пішло не так." {
		t.Fatalf("%+v", out)
	}
}

func TestToastUsesThePressersLanguage(t *testing.T) {
	e := inLanguage(t)
	raw, _ := json.Marshal(event.Decision{Button: "unknown", From: "uk", Ref: "1", Query: "q"})
	out := e.turn(t, event.Event{ID: "e", IdemKey: "k", Source: "telegram", Conv: "telegram:1", Kind: event.KindDecision, Payload: raw})
	if len(out) != 1 || out[0].Ack == "" {
		t.Fatalf("%+v", out)
	}
	if got := out[0].Text; got != "Ця кнопка більше не працює." {
		t.Fatalf("toast %q", got)
	}
}

func TestOpenQuestionKeepsItsText(t *testing.T) {
	langs := &fakeLanguages{lang: map[string]texts.Lang{}}
	base := newEnv(t, Config{}, nil)
	base.deps.Languages = langs
	e := newConfirmEnvOn(t, base, newSideTool())
	q, _ := e.ask(t, "schedule.create")
	if q.Buttons[0].Label != "✅ Approve" {
		t.Fatalf("label %q", q.Buttons[0].Label)
	}
	langs.lang[owner] = "uk" // the person changes language while the question is open
	e.smart.push(say("done"))
	out := e.turn(t, press(owner, q.Buttons[0].ID, "1"))
	var toast, folded string
	for _, o := range append(e.early.got(), out...) {
		if o.Ack != "" {
			toast = o.Text
		}
		if o.Folded != "" {
			folded = o.Folded
		}
	}
	if toast != "Дозволено" {
		t.Errorf("toast %q", toast)
	}
	if folded != q.Text {
		t.Errorf("the stored question changed: %q, want %q", folded, q.Text)
	}
}

// machineEnv reads Polish from a machine pack, Ukrainian from a shipped file and English.
func machineEnv(t *testing.T, packed map[texts.Key]string) *env {
	t.Helper()
	cat, err := texts.New(texts.WithPacks(func(l texts.Lang, k texts.Key) (string, bool) {
		s, ok := packed[k]
		return s, ok && l == "pl"
	}))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, Config{}, nil)
	e.deps.Languages = &fakeLanguages{lang: map[string]texts.Lang{"pl": "pl", "uk": "uk"}}
	e.deps.Texts = cat
	e.a = New(overDefaults(Config{}), e.deps)
	return e
}

const noticePL = "Tłumaczenie maszynowe."

func TestNoticeOnce(t *testing.T) {
	e := machineEnv(t, map[texts.Key]string{texts.ButtonApprove: "Zatwierdź", texts.NoticeMachine: noticePL})
	e.smart.push(say("one"), say("two"))
	first := e.turn(t, message("pl", "A", "hi"))
	if len(first) != 1 || first[0].Text != "one\n\n"+noticePL {
		t.Fatalf("first %+v", first)
	}
	second := e.turn(t, message("pl", "A", "hi again"))
	if len(second) != 1 || second[0].Text != "two" {
		t.Fatalf("second %+v", second)
	}
}

func TestNoticeNotForShipped(t *testing.T) {
	e := machineEnv(t, nil)
	e.smart.push(say("one"))
	if out := e.turn(t, message("uk", "A", "hi")); out[0].Text != "one" {
		t.Fatalf("%q", out[0].Text)
	}
}

func TestNoticeEnglishIfItsKeyFailed(t *testing.T) {
	e := machineEnv(t, map[texts.Key]string{texts.ButtonApprove: "Zatwierdź"})
	e.smart.push(say("one"))
	out := e.turn(t, message("pl", "A", "hi"))
	if want := "one\n\n" + texts.EnglishText(texts.NoticeMachine); out[0].Text != want {
		t.Fatalf("%q", out[0].Text)
	}
}

func TestNoticeSurvivesRerun(t *testing.T) {
	e := machineEnv(t, map[texts.Key]string{texts.ButtonApprove: "Zatwierdź", texts.NoticeMachine: noticePL})
	ev := message("pl", "A", "hi")
	e.smart.push(say("one"), say("one"))
	o1, _, err1 := e.a.Handle(context.Background(), ev)
	o2, _, err2 := e.a.Handle(context.Background(), ev) // a restart reruns the same event
	if err1 != nil || err2 != nil || o1[0].Text != o2[0].Text || !strings.HasSuffix(o2[0].Text, noticePL) {
		t.Fatalf("%q, %v; %q, %v", o1[0].Text, err1, o2[0].Text, err2)
	}
}

func TestNoticeWaitsForAMessage(t *testing.T) {
	e := machineEnv(t, map[texts.Key]string{texts.ButtonApprove: "Zatwierdź", texts.NoticeMachine: noticePL})
	raw, _ := json.Marshal(event.Decision{Button: "unknown", From: "pl", Ref: "1", Query: "q"})
	e.turn(t, event.Event{ID: "e", IdemKey: "k", Source: "telegram", Conv: "telegram:1", Kind: event.KindDecision, Payload: raw})
	e.smart.push(say("one"))
	if out := e.turn(t, message("pl", "A", "hi")); !strings.HasSuffix(out[0].Text, noticePL) {
		t.Fatalf("a toast used up the notice: %q", out[0].Text)
	}
}

func TestSystemJobGetsTheOwnersSpeaker(t *testing.T) {
	e := inLanguage(t)
	var got texts.Lang
	e.deps.SystemJobs = map[string]SystemJob{"system:x": func(_ context.Context, sp texts.Speaker) (string, error) {
		got = sp.Lang
		return "report", nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	tn := &turn{msg: event.Message{From: "uk"}}
	if err := e.a.system(context.Background(), tn, schedule.Job{ID: "system:x"}); err != nil {
		t.Fatal(err)
	}
	if got != "uk" || len(tn.out) != 1 {
		t.Fatalf("language %q, out %+v", got, tn.out)
	}
}
