package builtin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

func TestWebFetchQuestionShowsItsAddressAsIs(t *testing.T) {
	const url = "https://a.example/x_y*z"
	c := tool.Call{Name: "web.fetch", Args: json.RawMessage(`{"url":"` + url + `"}`), Speak: texts.Speaker{Lang: "uk"}}
	q, err := (&builtin.WebFetch{}).Question(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Відкрити цю сторінку?\n`" + url + "`"; q != want {
		t.Fatalf("%q, want %q", q, want)
	}
}

func TestMessageSendQuestionShowsItsTextAsIs(t *testing.T) {
	c := call("message.send", sendArgs("telegram:42", "**тут** _так_ {name}"))
	c.Speak = texts.Speaker{Lang: "uk"}
	q, err := confirmed(t, c, builtin.NewMessageSend(convs()))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n\n**тут** _так_ {name}"; len(q) < len(want) || q[len(q)-len(want):] != want {
		t.Fatalf("%q", q)
	}
	if q[:len("Надіслати")] != "Надіслати" {
		t.Fatalf("%q", q)
	}
}

func TestEnglishQuestionsAreUnchanged(t *testing.T) {
	c := tool.Call{Name: "web.fetch", Args: json.RawMessage(`{"url":"https://a.example/"}`)}
	q, err := (&builtin.WebFetch{}).Question(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if q != "Open this page?\n`https://a.example/`" {
		t.Fatalf("%q", q)
	}
}

func TestButtonsThatActCarrySymbols(t *testing.T) {
	e := newSchedEnv(t)
	e.add(t, rates("j1"))
	c := call("schedule.list", `{}`)
	c.Speak = texts.Speaker{Lang: "uk"}
	res, err := builtin.NewScheduleList(e.sc).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Out[0].Buttons
	if b[0].Label != "Докладніше" || b[1].Label != "🗑 Видалити" || b[2].Label != "▶️ Запустити" {
		t.Fatalf("%+v", b)
	}
}

// Review of task 5: a colon after the id read as "key: value" to the redaction, so the same call
// was refused in Ukrainian and Russian and asked in English.
func TestCellQuestionWithASecretLikeIDIsAskedInEveryLanguage(t *testing.T) {
	for _, l := range []texts.Lang{texts.English, "ru", "uk"} {
		c := call("cells.set", `{"id":"door.key","value":"on"}`)
		c.Speak = texts.Speaker{Lang: l}
		if _, err := (&builtin.CellsSet{}).Question(context.Background(), c); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
}

func TestSystemJobTextIsShownInTheLanguage(t *testing.T) {
	e := newSchedEnv(t)
	audit := remind(builtin.AuditJobID)
	audit.Kind, audit.System, audit.Text = schedule.KindSystem, true, "stored English text"
	e.add(t, audit)
	c := call("schedule.list", `{}`)
	c.Speak = texts.Speaker{Lang: "uk"}
	res, err := builtin.NewScheduleList(e.sc).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Out[0].Text; strings.Contains(got, "stored English text") || !strings.Contains(got, "Перевірка памʼяті") {
		t.Errorf("shown %q", got)
	}
	if !strings.Contains(res.Text, "stored English text") {
		t.Errorf("the model should read the stored text: %q", res.Text)
	}
	if strings.Contains(strings.ToLower(res.Text), "task") {
		t.Errorf("a system job must not read as a task to the model: %q", res.Text)
	}
}
