package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/pending"
)

const me = "telegram:user:42"

// t0 is 10:00 on a Tuesday in Kyiv.
var t0 = time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

// inbox records the events the scheduler puts; one per key.
type inbox struct {
	mu     sync.Mutex
	events []event.Event
}

func (q *inbox) Put(_ context.Context, e event.Event) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, old := range q.events {
		if old.IdemKey == e.IdemKey {
			return nil
		}
	}
	q.events = append(q.events, e)
	return nil
}

type schedEnv struct {
	sc      builtin.Scheduling
	jobs    *schedule.Store
	pending *pending.Store
	q       *inbox
	specs   map[string]tool.Spec
}

func newSchedEnv(t *testing.T) *schedEnv {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := schedule.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return t0 }
	ps, err := pending.New(ctx, db, cells.NewService(cells.NewMemStore(), cells.WithClock(now)), now, pending.DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	e := &schedEnv{jobs: st, pending: ps, q: &inbox{}, specs: map[string]tool.Spec{
		"web.fetch":    {Name: "web.fetch", Class: tool.ClassExternal, Policy: tool.PolicyAuto},
		"message.send": {Name: "message.send", Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm},
		"cells.get":    {Name: "cells.get", Class: tool.ClassRead, Policy: tool.PolicyAuto},
		"shell.exec":   {Name: "shell.exec", Class: tool.ClassSideEffect, Policy: tool.PolicyForbidden},
	}}
	e.sc = builtin.Scheduling{
		Jobs:    st,
		Runner:  schedule.New(st, e.q, kyiv, now),
		Specs:   func(name string) (tool.Spec, bool) { s, ok := e.specs[name]; return s, ok },
		Buttons: ps,
		Convs:   convs(),
		Loc:     kyiv,
		Now:     now,
	}
	return e
}

const ratesArgs = `{"kind":"task","cron":"0 * * * *","text":"send me the exchange rates, compared with the previous runs",` +
	`"grant":{"tools":["web.fetch"],"hosts":["bank.gov.ua"]}}`

func TestScheduleCreateQuestionShowsTheGrant(t *testing.T) {
	e := newSchedEnv(t)
	q, err := builtin.NewScheduleCreate(e.sc).Question(context.Background(), call("schedule.create", ratesArgs))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"send me the exchange rates, compared with the previous runs", // the instruction
		`"0 * * * *"`, "Europe/Kyiv", "2026-11-03 11:00", // the schedule and its next run
		"web.fetch",   // the tools
		"bank.gov.ua", // the hosts
		"this chat",   // who receives the reply
	} {
		if !strings.Contains(q, want) {
			t.Errorf("the question lacks %q:\n%s", want, q)
		}
	}
	q, err = builtin.NewScheduleCreate(e.sc).Question(context.Background(),
		call("schedule.create", `{"kind":"task","at":"2026-11-03 15:30","text":"check the boiler","grant":{"tools":["cells.get"],"reply_to":"telegram:user:7"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"once at 2026-11-03 15:30", "cells.get", "Hosts web.fetch may reach: none", "telegram:7"} {
		if !strings.Contains(q, want) {
			t.Errorf("the question lacks %q:\n%s", want, q)
		}
	}
	q, err = builtin.NewScheduleCreate(e.sc).Question(context.Background(),
		call("schedule.create", `{"kind":"reminder","cron":"0 9 * * 1-5","text":"stand-up"}`))
	if err != nil || !strings.Contains(q, "reminder") || !strings.Contains(q, "stand-up") || strings.Contains(q, "Tools") {
		t.Fatalf("reminder question %q, %v", q, err)
	}
}

// A grant or a time that cannot be honoured fails closed, before anyone
// is asked.
func TestScheduleCreateValidates(t *testing.T) {
	e := newSchedEnv(t)
	create := builtin.NewScheduleCreate(e.sc)
	long := strings.Repeat("a", 1001)
	for name, args := range map[string]string{
		"not json":               `{`,
		"an unknown field":       `{"kind":"task","cron":"0 * * * *","text":"x","every":"hour"}`,
		"unknown kind":           `{"kind":"alarm","cron":"0 * * * *","text":"x"}`,
		"no text":                `{"kind":"task","cron":"0 * * * *","text":"  "}`,
		"a text too long":        `{"kind":"task","cron":"0 * * * *","text":"` + long + `"}`,
		"no time":                `{"kind":"task","text":"x"}`,
		"cron and at":            `{"kind":"task","cron":"0 * * * *","at":"2026-11-04 10:00","text":"x"}`,
		"bad cron":               `{"kind":"task","cron":"0 * * * MON","text":"x"}`,
		"a cron that never runs": `{"kind":"task","cron":"0 0 31 2 *","text":"x"}`,
		"a time in the past":     `{"kind":"task","at":"2026-11-03 09:00","text":"x"}`,
		"a time that is no time": `{"kind":"task","at":"tomorrow","text":"x"}`,
		"an unknown tool":        `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["rm.rf"]}}`,
		"a forbidden tool":       `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["shell.exec"]}}`,
		"a task that schedules":  `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["schedule.create"]}}`,
		"a host with a scheme":   `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"],"hosts":["https://bank.gov.ua"]}}`,
		"a host with a path":     `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"],"hosts":["bank.gov.ua/rates"]}}`,
		"a host with a port":     `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"],"hosts":["bank.gov.ua:443"]}}`,
		"a wildcard host":        `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"],"hosts":["*.gov.ua"]}}`,
		"an upper-case host":     `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"],"hosts":["Bank.gov.ua"]}}`,
		"a bare name":            `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"],"hosts":["localhost"]}}`,
		"hosts without fetch":    `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["cells.get"],"hosts":["bank.gov.ua"]}}`,
		"fetch without hosts":    `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"tools":["web.fetch"]}}`,
		"an unknown reply":       `{"kind":"task","cron":"0 * * * *","text":"x","grant":{"reply_to":"telegram:999"}}`,
		"a reminder with tools":  `{"kind":"reminder","cron":"0 * * * *","text":"x","grant":{"tools":["cells.get"]}}`,
	} {
		c := call("schedule.create", args)
		if _, err := create.Question(context.Background(), c); err == nil {
			t.Errorf("%s: asked about %s", name, args)
		}
		if _, err := create.Run(context.Background(), c); err == nil {
			t.Errorf("%s: created %s", name, args)
		}
	}
	if list, _ := e.jobs.List(context.Background(), me); len(list) != 0 {
		t.Fatalf("invalid jobs stored: %+v", list)
	}
}

func TestScheduleCreateRuns(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	create := builtin.NewScheduleCreate(e.sc)
	c := call("schedule.create", ratesArgs)
	for range 2 { // a rerun of the approved call creates the job once
		res, err := create.Run(ctx, c)
		if err != nil || !strings.Contains(res.Text, "e1-2") || len(res.Out) != 0 {
			t.Fatalf("run = %+v, %v", res, err)
		}
	}
	list, err := e.jobs.List(ctx, me)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v, %v", list, err)
	}
	j := list[0]
	if j.ID != "e1-2" || j.Owner != me || j.Conv != "telegram:42" || j.Transport != "telegram" || j.Kind != schedule.KindTask ||
		j.Cron != "0 * * * *" || !j.Created.Equal(t0) || !slices.Equal(j.Grant.Tools, []string{"web.fetch"}) ||
		!slices.Equal(j.Grant.Hosts, []string{"bank.gov.ua"}) || j.Grant.ReplyTo != "telegram:42" {
		t.Fatalf("job %+v", j)
	}
	if !strings.Contains(create.Spec().Description, "grant") || create.Spec().Class != tool.ClassSideEffect || !create.Spec().AdminOnly {
		t.Fatalf("spec %+v", create.Spec())
	}
}

// Creating goes through the owner's press, asked in the tool's words.
func TestScheduleCreateNeedsConfirm(t *testing.T) {
	e := newSchedEnv(t)
	create := builtin.NewScheduleCreate(e.sc)
	reg, err := tool.NewRegistry(router.Local{}, create)
	if err != nil {
		t.Fatal(err)
	}
	ask := &asker{}
	run := tool.Chain(reg.Run, policy.New(policy.DefaultConfig(), reg, ask).Middleware())
	res, err := run(context.Background(), call("schedule.create", ratesArgs))
	if err != nil || res.Pending != "pend-1" || len(ask.asked) != 1 || !strings.Contains(ask.asked[0], "bank.gov.ua") {
		t.Fatalf("result %+v, %v; asked %q", res, err, ask.asked)
	}
	if list, _ := e.jobs.List(context.Background(), me); len(list) != 0 {
		t.Fatal("created before the press")
	}
}

func (e *schedEnv) add(t *testing.T, j schedule.Job) {
	t.Helper()
	if err := e.jobs.Ensure(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}

func rates(id string) schedule.Job {
	return schedule.Job{ID: id, Owner: me, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindTask, Cron: "0 * * * *",
		Text: "send me the exchange rates", Grant: tool.Grant{Tools: []string{"web.fetch"}, Hosts: []string{"bank.gov.ua"}, ReplyTo: "telegram:42"},
		Created: t0}
}

func remind(id string) schedule.Job {
	return schedule.Job{ID: id, Owner: me, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindReminder,
		At: t0.Add(5 * time.Hour), Text: "call mum", Created: t0.Add(time.Second)}
}

func labels(bs []event.Button) string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Label)
	}
	return strings.Join(out, ",")
}

// pressAs presses a button the way the agent does and runs its action.
func (e *schedEnv) pressAs(t *testing.T, b event.Button, at time.Time) pending.QuickEdit {
	t.Helper()
	ctx := context.Background()
	p, err := e.pending.Press(ctx, b.ID, pending.By{Principal: me, Conv: "telegram:42", Event: "PRESS-" + b.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, ok := e.sc.QuickActions()[p.Button.Action]
	if !ok {
		t.Fatalf("no quick action %q", p.Button.Action)
	}
	edit, err := run(ctx, pending.QuickPress{Button: p.Button, Transport: "telegram", At: at})
	if err != nil {
		t.Fatal(err)
	}
	return edit
}

func TestListHasButtons(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	list := builtin.NewScheduleList(e.sc)
	res, err := list.Run(ctx, call("schedule.list", `{}`))
	if err != nil || len(res.Out) != 0 || !strings.Contains(res.Text, "no scheduled") {
		t.Fatalf("empty list = %+v, %v", res, err)
	}
	e.add(t, rates("j1"))
	e.add(t, remind("r1"))
	audit := rates("system:memory-audit")
	audit.System, audit.Created = true, t0.Add(2*time.Second)
	e.add(t, audit)
	res, err = list.Run(ctx, call("schedule.list", `{}`))
	if err != nil || len(res.Out) != 3 {
		t.Fatalf("list = %+v, %v", res, err)
	}
	for i, want := range []struct{ id, labels, text string }{
		{"j1", "Details,Delete,Run now", "send me the exchange rates"},
		{"r1", "Details,Delete", "call mum"},
		{"system:memory-audit", "Details,Run now", "(system)"},
	} {
		o := res.Out[i]
		if o.IdemKey != "E1:2:list:"+want.id || o.Conv != "telegram:42" || o.Transport != "telegram" || labels(o.Buttons) != want.labels ||
			!strings.Contains(o.Text, want.text) {
			t.Errorf("entry %d: %+v", i, o)
		}
		if !strings.Contains(res.Text, want.id) {
			t.Errorf("the model does not read %s in %q", want.id, res.Text)
		}
	}
	// The buttons are stored for the person who listed, in that chat.
	edit := e.pressAs(t, res.Out[0].Buttons[0], t0)
	if !strings.Contains(edit.Text, "bank.gov.ua") || !strings.Contains(edit.Text, "Next run") || labels(edit.Buttons) != "Delete,Run now" {
		t.Fatalf("details %+v", edit)
	}
	if spec := list.Spec(); spec.Class != tool.ClassRead || !spec.AdminOnly {
		t.Fatalf("spec %+v", spec)
	}
}

// Delete by button needs no second press; a system job stays.
func TestDeleteQuickAction(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	e.add(t, rates("j1"))
	audit := rates("system:memory-audit")
	audit.System = true
	e.add(t, audit)
	res, err := builtin.NewScheduleList(e.sc).Run(ctx, call("schedule.list", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	edit := e.pressAs(t, res.Out[0].Buttons[1], t0)
	if !strings.Contains(edit.Text, "Deleted") || len(edit.Buttons) != 0 {
		t.Fatalf("edit %+v", edit)
	}
	if j, _ := e.jobs.Get(ctx, "j1"); !j.Deleted {
		t.Fatal("not deleted")
	}
	// Details of a deleted job, from an older button.
	if edit := e.pressAs(t, res.Out[0].Buttons[0], t0); !strings.Contains(edit.Text, "no longer") || len(edit.Buttons) != 0 {
		t.Fatalf("details of a deleted job %+v", edit)
	}
	// A system job's Delete, should a button exist.
	b := pending.Button{ID: pending.NewButtonID(), Action: "schedule.delete", Label: "Delete", Principal: me, Conv: "telegram:42",
		Args: json.RawMessage(`{"id":"system:memory-audit"}`), Expires: t0.Add(time.Hour)}
	if err := e.pending.AddButtons(ctx, b); err != nil {
		t.Fatal(err)
	}
	if edit := e.pressAs(t, event.Button{ID: b.ID}, t0); !strings.Contains(edit.Text, "cannot be deleted") {
		t.Fatalf("system delete %+v", edit)
	}
}

func TestRunNowQuickAction(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	reads := rates("j1")
	acts := rates("j2")
	acts.Grant.Tools = []string{"message.send"}
	acts.Grant.Hosts = nil
	e.add(t, reads)
	e.add(t, acts)
	res, err := builtin.NewScheduleList(e.sc).Run(ctx, call("schedule.list", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	// A task that only reads runs at once.
	at := t0.Add(time.Minute)
	edit := e.pressAs(t, res.Out[0].Buttons[2], at)
	if !strings.Contains(edit.Text, "Started") || labels(edit.Buttons) != "Details,Delete,Run now" {
		t.Fatalf("edit %+v", edit)
	}
	if len(e.q.events) != 1 || e.q.events[0].IdemKey != "cron:j1:now:"+strconv.FormatInt(at.UnixMilli(), 10) {
		t.Fatalf("events %+v", e.q.events)
	}
	// A task that may act asks first, in place.
	edit = e.pressAs(t, res.Out[1].Buttons[2], at)
	if !strings.Contains(edit.Text, "message.send") || labels(edit.Buttons) != "Approve,Deny" || len(e.q.events) != 1 {
		t.Fatalf("edit %+v, events %d", edit, len(e.q.events))
	}
	p, err := e.pending.Press(ctx, edit.Buttons[0].ID, pending.By{Principal: me, Conv: "telegram:42", Event: "P2"})
	if err != nil || p.Item.Tool != "schedule.run_now" || string(p.Item.Args) != `{"id":"j2"}` {
		t.Fatalf("press %+v, %v", p, err)
	}
	// The approved call runs the tool.
	runNow := builtin.NewScheduleRunNow(e.sc)
	c := p.Item.Call()
	c.Admin = true
	if _, err := runNow.Run(ctx, c); err != nil || len(e.q.events) != 2 || e.q.events[1].Conv != "task:j2" {
		t.Fatalf("run now: %v, events %+v", err, e.q.events)
	}
}

func TestScheduleDeleteAndRunNowTools(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	e.add(t, rates("j1"))
	audit := rates("system:memory-audit")
	audit.System = true
	e.add(t, audit)
	del, runNow := builtin.NewScheduleDelete(e.sc), builtin.NewScheduleRunNow(e.sc)
	for _, h := range []tool.Handler{del, runNow} {
		if s := h.Spec(); s.Class != tool.ClassSideEffect || !s.AdminOnly {
			t.Fatalf("spec %+v", s)
		}
	}
	q, err := del.Question(ctx, call("schedule.delete", `{"id":"j1"}`))
	if err != nil || !strings.Contains(q, "send me the exchange rates") {
		t.Fatalf("question %q, %v", q, err)
	}
	if _, err := del.Question(ctx, call("schedule.delete", `{"id":"nope"}`)); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("question about an unknown job: %v", err)
	}
	if _, err := del.Run(ctx, call("schedule.delete", `{"id":"system:memory-audit"}`)); !errors.Is(err, schedule.ErrSystem) {
		t.Fatalf("delete a system job: %v", err)
	}
	if q, err := runNow.Question(ctx, call("schedule.run_now", `{"id":"j1"}`)); err != nil || !strings.Contains(q, "now") {
		t.Fatalf("run now question %q, %v", q, err)
	}
	if _, err := runNow.Run(ctx, call("schedule.run_now", `{"id":"j1"}`)); err != nil || len(e.q.events) != 1 {
		t.Fatalf("run now: %v", err)
	}
	if _, err := del.Run(ctx, call("schedule.delete", `{"id":"j1"}`)); err != nil {
		t.Fatal(err)
	}
	if j, _ := e.jobs.Get(ctx, "j1"); !j.Deleted {
		t.Fatal("not deleted")
	}
}
