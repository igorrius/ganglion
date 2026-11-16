package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/memory"
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
	c := call("schedule.list", `{"show":true}`)
	c.Speak = texts.Speaker{Lang: "uk"}
	res, err := builtin.NewScheduleList(e.sc).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Out[0].Buttons
	if b[0].Label != "Докладніше" || b[1].Label != "🗑 Видалити" || b[2].Label != "▶️ Запустити" {
		t.Fatalf("%+v", b)
	}
	hourly := rates("r1")
	hourly.Kind, hourly.Grant = schedule.KindReminder, tool.Grant{}
	done, err := e.sc.ReminderButtons(context.Background(), texts.Speaker{Lang: "uk"}, hourly)
	if err != nil || len(done) != 1 || done[0].Label != "✅ Виконано" {
		t.Fatalf("%+v, %v", done, err)
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
	c := call("schedule.list", `{"show":true}`)
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

type fakeAudit struct {
	builtin.Facts
	rep memory.Report
}

func (f fakeAudit) Audit(context.Context) (memory.Report, error) { return f.rep, nil }

func TestAuditReportFollowsTheOwnersLanguage(t *testing.T) {
	sp := texts.Speaker{Lang: "uk"}
	clean := builtin.NewAuditJob(fakeAudit{rep: memory.Report{Facts: 3, Persona: 1}})
	got, err := clean.Run(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Нічна перевірка памʼяті.") || !strings.Contains(got, "3") {
		t.Errorf("clean: %q", got)
	}
	flagged := builtin.NewAuditJob(fakeAudit{rep: memory.Report{Facts: 3, Persona: 1,
		Findings: []memory.Finding{{Path: "memory/car.md", Reason: "looks like an instruction"}}}})
	got, err = flagged.Run(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "\n- memory/car.md: looks like an instruction") || strings.Contains(got, "Checked") {
		t.Errorf("flagged: %q", got)
	}
}

func TestAuditToolResultStaysEnglish(t *testing.T) {
	c := call("memory.audit", `{}`)
	c.Speak = texts.Speaker{Lang: "uk"}
	var audit tool.Handler
	for _, h := range builtin.MemoryTools(fakeAudit{rep: memory.Report{Facts: 3}}, nil, builtin.MemoryNotes{}) {
		if h.Spec().Name == "memory.audit" {
			audit = h
		}
	}
	res, err := audit.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Checked 3 stored facts") {
		t.Errorf("%q", res.Text)
	}
}

// Review of task 7: a padded summary pushed the real instruction past the policy's silent cut.
func TestScheduleQuestionTooLongIsRefused(t *testing.T) {
	cat, err := texts.New(texts.WithFiles(map[texts.Lang]map[texts.Key]string{"pl": {
		texts.SchedSummaryReminder: strings.Repeat("Tylko przypomnienie o pogodzie. ", 110) + "{text} {timing} {target}",
	}}))
	if err != nil {
		t.Fatal(err)
	}
	e := newSchedEnv(t)
	create := builtin.NewScheduleCreate(e.sc)
	c := call("schedule.create", `{"kind":"reminder","at":"2026-12-01T09:00","text":"call mum"}`)
	c.Speak = texts.Speaker{Lang: "pl", Cat: cat}
	if _, err := create.Question(context.Background(), c); !errors.Is(err, builtin.ErrTooLongToShow) {
		t.Fatalf("err %v", err)
	}
}
