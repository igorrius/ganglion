package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/transport"
)

func (e *env) userTexts(t *testing.T, call int) []string {
	t.Helper()
	var out []string
	for _, m := range e.smart.requests()[call].Messages {
		if m.Role == "user" {
			text, _, _ := strings.Cut(m.Content, "\n[now: ") // the line the turn appends
			out = append(out, text)
		}
	}
	return out
}

func TestIdleStartsNewSession(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	first := message("telegram:user:42", "Igor", "вечер")
	e.turn(t, first)
	e.now = t0.Add(7 * time.Hour)
	morning := message("telegram:user:42", "Igor", "утро")
	e.turn(t, morning)
	if got := e.userTexts(t, 1); len(got) != 1 || got[0] != "Igor: утро" {
		t.Fatalf("after 7 h of silence the model saw %q", got)
	}
	if st := e.state(t); st.Session != morning.ID || !st.Started.Equal(e.now) {
		t.Fatalf("new session %+v", st)
	}
	list, _ := e.archive.List(context.Background(), "telegram:42", 5, "")
	if len(list) != 1 || list[0].Session != first.ID || list[0].Turns != 1 || list[0].Title != "Igor: вечер" {
		t.Fatalf("archive %+v", list)
	}
}

func TestWithinIdleKeepsSession(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "раз"))
	e.now = t0.Add(5 * time.Hour)
	e.turn(t, message("telegram:user:42", "Igor", "два"))
	if got := e.userTexts(t, 1); len(got) != 2 {
		t.Fatalf("within 6 h the model saw %q", got)
	}
}

func TestNewArchivesAndResets(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "раз"))
	out := e.turn(t, message("telegram:user:42", "Igor", "/new"))
	if len(out) != 1 || out[0].Text != "New session started." {
		t.Fatalf("out %+v", out)
	}
	if len(e.smart.requests()) != 1 {
		t.Fatal("/new called the model")
	}
	if st := e.state(t); st.Session != "" || len(st.Messages) != 0 {
		t.Fatalf("state after /new %+v; the next message opens the session", st)
	}
	if list, _ := e.archive.List(context.Background(), "telegram:42", 5, ""); len(list) != 1 {
		t.Fatalf("archive %+v", list)
	}
	e.turn(t, message("telegram:user:42", "Igor", "два"))
	if got := e.userTexts(t, 1); len(got) != 1 {
		t.Fatalf("after /new the model saw %q", got)
	}
}

func TestNewOnEmptySessionArchivesNothing(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "/reset"))
	if list, _ := e.archive.List(context.Background(), "telegram:42", 5, ""); len(list) != 0 {
		t.Fatalf("archive %+v", list)
	}
}

func TestCommandWithBotSuffix(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "раз"))
	if out := e.turn(t, message("telegram:user:42", "Igor", "/new@ganglion_test_bot")); out[0].Text != "New session started." {
		t.Fatalf("out %+v", out)
	}
}

func TestSessionsList(t *testing.T) {
	e := newEnv(t, Config{Location: time.UTC}, nil)
	if out := e.turn(t, message("telegram:user:42", "Igor", "/sessions")); out[0].Text != "No earlier sessions." {
		t.Fatalf("empty list %q", out[0].Text)
	}
	e.turn(t, message("telegram:user:42", "Igor", "про бойлер"))
	e.now = t0.Add(5 * time.Minute)
	e.turn(t, message("telegram:user:42", "Igor", "ещё"))
	e.turn(t, message("telegram:user:42", "Igor", "/new"))
	e.now = t0.Add(time.Hour)
	e.turn(t, message("telegram:user:42", "Anna", "про ужин"))
	e.turn(t, message("telegram:user:42", "Anna", "/new"))
	out := e.turn(t, message("telegram:user:42", "Igor", "/sessions"))
	want := "1. 3 Nov 11:00–11:00 · 1 turns · Anna: про ужин\n2. 3 Nov 10:00–10:05 · 2 turns · Igor: про бойлер"
	if out[0].Text != want {
		t.Fatalf("list\n got %q\nwant %q", out[0].Text, want)
	}
}

func TestResumeSwapsSessions(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "про бойлер"))
	e.turn(t, message("telegram:user:42", "Igor", "/new"))
	e.turn(t, message("telegram:user:42", "Igor", "про ужин"))
	out := e.turn(t, message("telegram:user:42", "Igor", "/resume 1"))
	if out[0].Text != "Resumed: Igor: про бойлер" {
		t.Fatalf("out %q", out[0].Text)
	}
	e.turn(t, message("telegram:user:42", "Igor", "и что с ним?"))
	got := e.userTexts(t, 2)
	if strings.Join(got, "|") != "Igor: про бойлер|Igor: и что с ним?" {
		t.Fatalf("after /resume the model saw %q", got)
	}
	list, _ := e.archive.List(context.Background(), "telegram:42", 5, "")
	titles := []string{}
	for _, s := range list {
		titles = append(titles, s.Title)
	}
	if strings.Join(titles, "|") != "Igor: про ужин|Igor: про бойлер" {
		t.Fatalf("archive after /resume %q", titles)
	}
}

func TestResumeBadIndex(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	for _, cmd := range []string{"/resume 9", "/resume x", "/resume"} {
		if out := e.turn(t, message("telegram:user:42", "Igor", cmd)); out[0].Text != "No such session; see /sessions." {
			t.Fatalf("%s: %q", cmd, out[0].Text)
		}
	}
}

func TestArchiveRerunIsIdempotent(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	ctx := context.Background()
	st := State{V: 1, Session: "S1", Started: t0, Last: t0}
	for range 2 {
		if err := e.archive.Put(ctx, "telegram:42", st, "new"); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := e.archive.List(ctx, "telegram:42", 5, ""); len(list) != 1 {
		t.Fatalf("%d rows after a rerun", len(list))
	}
}

func TestSessionsListExcludesTheCurrentSession(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "A1"))
	e.turn(t, message("telegram:user:42", "Igor", "/new"))
	e.turn(t, message("telegram:user:42", "Igor", "B1"))
	e.turn(t, message("telegram:user:42", "Igor", "/resume 1")) // back on A, B archived
	e.turn(t, message("telegram:user:42", "Igor", "A2"))
	if out := e.turn(t, message("telegram:user:42", "Igor", "/sessions")); strings.Contains(out[0].Text, "A1") {
		t.Fatalf("the current session is offered as an earlier one:\n%s", out[0].Text)
	}
	e.turn(t, message("telegram:user:42", "Igor", "/resume 2"))
	var users []string
	for _, m := range e.state(t).Messages {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	if strings.Join(users, "|") != "Igor: A1|Igor: A2" {
		t.Fatalf("/resume of a missing index changed the session: %q", users)
	}
}

func TestResumeRerunPicksTheSameSession(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	for _, txt := range []string{"A1", "/new", "B1", "/new", "C1"} {
		e.turn(t, message("telegram:user:42", "Igor", txt))
	}
	ev := message("telegram:user:42", "Igor", "/resume 2")
	o1, _, err1 := e.a.Handle(context.Background(), ev)
	o2, s2, err2 := e.a.Handle(context.Background(), ev) // the same event again, as after a crash
	if err1 != nil || err2 != nil || o1[0].Text != o2[0].Text || o1[0].Text != "Resumed: Igor: A1" {
		t.Fatalf("first %q, rerun %q", o1[0].Text, o2[0].Text)
	}
	st, _ := DecodeState(s2.Data)
	if st.Messages[0].Content != "Igor: A1" {
		t.Fatalf("rerun state %+v", st.Messages)
	}
}

// The command hints come from the command table, the one source: every
// listed command is one the agent answers, described in English (the
// default), Russian and Ukrainian; an alias is not listed (rulings R43,
// R45).
func TestCommandHintsComeFromTheTable(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	cmds := e.a.Commands()
	var names []string
	for _, c := range cmds {
		names = append(names, c.Name)
		if _, ok := e.a.commands["/"+c.Name]; !ok {
			t.Errorf("/%s is listed but not answered", c.Name)
		}
		for _, l := range []transport.Lang{transport.LangDefault, transport.LangRussian, transport.LangUkrainian} {
			if d := c.Description[l]; d == "" || len([]rune(d)) > 256 {
				t.Errorf("/%s in %q: description %q", c.Name, l, d)
			}
		}
	}
	if strings.Join(names, ",") != "new,sessions,resume" {
		t.Fatalf("listed %q", names)
	}
	e.smart.push()
	if out := e.turn(t, message(owner, "Igor", "/reset")); len(out) != 1 || out[0].Text != replyNewSession {
		t.Fatalf("/reset: %+v", out)
	}
}
