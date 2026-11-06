package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// fakeLanguages is a person's language kept in a map.
type fakeLanguages struct {
	seen map[string]string
	lang map[string]texts.Lang
	err  error
}

func (f *fakeLanguages) Seen(_ context.Context, principal, code string) error {
	if f.seen == nil {
		f.seen = map[string]string{}
	}
	f.seen[principal] = code
	return f.err
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
