package policy_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
)

func spec(name string, class tool.Class, pol tool.Policy) tool.Spec {
	return tool.Spec{Name: name, Class: class, Policy: pol, AdminOnly: true,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{},"conv":{},"text":{},"id":{},"token":{}}}`)}
}

func args(kv ...string) json.RawMessage {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return b
}

func known(urls ...string) tool.URLFunc {
	return func(u string) bool { return slices.Contains(urls, u) }
}

// base is an admin's clean call in conversation "c1".
func base() tool.Call {
	return tool.Call{Conv: "c1", Principal: "p", Admin: true, KnownURL: known(), Args: json.RawMessage(`{}`)}
}

func with(c tool.Call, f func(*tool.Call)) tool.Call { f(&c); return c }

func TestDecide(t *testing.T) {
	read := spec("clock", tool.ClassRead, tool.PolicyAuto)
	side := spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)
	sideAuto := spec("odd.side", tool.ClassSideEffect, tool.PolicyAuto)
	denied := spec("danger", tool.ClassRead, tool.PolicyForbidden)
	memory := spec("memory.write", tool.ClassSideEffect, tool.PolicyAuto)
	forget := spec("memory.forget", tool.ClassSideEffect, tool.PolicyAuto)
	send := spec("message.send", tool.ClassRead, tool.PolicyAuto)
	fetch := spec("web.fetch", tool.ClassExternal, tool.PolicyAuto)
	grant := &tool.Grant{Tools: []string{"clock", "web.fetch", "schedule.create"}, Hosts: []string{"Example.com"}}
	sendGrant := &tool.Grant{Tools: []string{"message.send"}, ReplyTo: "telegram:42"}

	for _, tc := range []struct {
		name string
		s    tool.Spec
		c    tool.Call
		pol  tool.Policy
		dec  journal.Decision
	}{
		{"read tool is auto", read, base(), tool.PolicyAuto, journal.DecisionAllowed},
		{"depth at the limit is fine", read, with(base(), func(c *tool.Call) { c.Depth = 3 }), tool.PolicyAuto, journal.DecisionAllowed},
		{"depth 4 is forbidden", read, with(base(), func(c *tool.Call) { c.Depth = 4 }), tool.PolicyForbidden, journal.DecisionForbidden},
		{"depth beats a grant", read, with(base(), func(c *tool.Call) { c.Depth = 4; c.Grant = grant }), tool.PolicyForbidden, journal.DecisionForbidden},
		{"family principal on an admin-only tool", read, with(base(), func(c *tool.Call) { c.Admin = false }), tool.PolicyForbidden, journal.DecisionForbidden},
		{"family principal on an open tool", tool.Spec{Name: "clock", Class: tool.ClassRead, Policy: tool.PolicyAuto}, with(base(), func(c *tool.Call) { c.Admin = false }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant: tool not granted", spec("memory.write", tool.ClassRead, tool.PolicyAuto), with(base(), func(c *tool.Call) { c.Grant = grant }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: tool granted", read, with(base(), func(c *tool.Call) { c.Grant = grant }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant: fetch on a granted host", fetch, with(base(), func(c *tool.Call) { c.Grant = grant; c.Args = args("url", "HTTPS://EXAMPLE.com:443/a#x") }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant: fetch on another host", fetch, with(base(), func(c *tool.Call) { c.Grant = grant; c.Args = args("url", "https://evil.example.org/") }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: fetch on a subdomain (no wildcards)", fetch, with(base(), func(c *tool.Call) { c.Grant = grant; c.Args = args("url", "https://a.example.com/") }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: fetch on another port", fetch, with(base(), func(c *tool.Call) { c.Grant = grant; c.Args = args("url", "https://example.com:8443/") }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: fetch with a bad url", fetch, with(base(), func(c *tool.Call) { c.Grant = grant; c.Args = args("url", "not a url") }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: fetch without a url", fetch, with(base(), func(c *tool.Call) { c.Grant = grant; c.Args = json.RawMessage(`{`) }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: no hosts means none", fetch, with(base(), func(c *tool.Call) {
			c.Grant = &tool.Grant{Tools: []string{"web.fetch"}}
			c.Args = args("url", "https://example.com/")
		}), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: send to the reply target", send, with(base(), func(c *tool.Call) {
			c.Grant, c.Tainted = sendGrant, true
			c.Args = args("conv", "telegram:42")
		}), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant: send to its own conv", send, with(base(), func(c *tool.Call) { c.Grant = sendGrant; c.Args = args("conv", "c1") }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant: send without a conv", send, with(base(), func(c *tool.Call) { c.Grant = sendGrant; c.Args = args("text", "hi") }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant: send elsewhere", send, with(base(), func(c *tool.Call) {
			c.Grant, c.Tainted = sendGrant, true
			c.Args = args("conv", "telegram:7")
		}), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: send to a principal", send, with(base(), func(c *tool.Call) { c.Grant = sendGrant; c.Args = args("conv", "telegram:user:7") }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: send with a conv that is not a string", send, with(base(), func(c *tool.Call) {
			c.Grant = sendGrant
			c.Args = json.RawMessage(`{"conv":["telegram:42"]}`)
		}), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant: send with unparsable arguments", send, with(base(), func(c *tool.Call) { c.Grant = sendGrant; c.Args = json.RawMessage(`[1]`) }), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"grant covers a side effect in a tainted session (R-POL-8)", side, with(base(), func(c *tool.Call) { c.Grant = grant; c.Tainted = true }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant naming a forbidden tool is still forbidden", denied, with(base(), func(c *tool.Call) { c.Grant = &tool.Grant{Tools: []string{"danger"}} }), tool.PolicyForbidden, journal.DecisionForbidden},
		{"memory forget, clean, is auto", forget, base(), tool.PolicyAuto, journal.DecisionAllowed},
		{"forbidden tool", denied, base(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"side effect confirms", side, base(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"side effect confirms even if the tool says auto", sideAuto, base(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"memory write, clean, is auto", memory, base(), tool.PolicyAuto, journal.DecisionAllowed},
		{"language, clean, is auto", spec("language.set", tool.ClassSideEffect, tool.PolicyAuto), base(), tool.PolicyAuto, journal.DecisionAllowed},
		{"language, tainted, confirms", spec("language.set", tool.ClassSideEffect, tool.PolicyAuto), with(base(), func(c *tool.Call) { c.Tainted = true }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"memory write, tainted, confirms", memory, with(base(), func(c *tool.Call) { c.Tainted = true }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"memory forget, tainted, confirms", forget, with(base(), func(c *tool.Call) { c.Tainted = true }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"tainted read tool is auto", read, with(base(), func(c *tool.Call) { c.Tainted = true }), tool.PolicyAuto, journal.DecisionAllowed},
		{"send to the same conv is auto", send, with(base(), func(c *tool.Call) { c.Args = args("conv", "c1") }), tool.PolicyAuto, journal.DecisionAllowed},
		{"send to another conv confirms", send, with(base(), func(c *tool.Call) { c.Args = args("conv", "c2") }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"send without a conv confirms", send, base(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"send with unparsable arguments confirms", send, with(base(), func(c *tool.Call) { c.Args = json.RawMessage(`[1]`) }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"fetch of a known url is auto", fetch, with(base(), func(c *tool.Call) {
			c.KnownURL = known("https://example.com/a")
			c.Args = args("url", "https://example.com/a")
		}), tool.PolicyAuto, journal.DecisionAllowed},
		{"fetch of an unknown url confirms", fetch, with(base(), func(c *tool.Call) { c.Args = args("url", "https://example.com/a?q=secret") }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"fetch without a url confirms", fetch, with(base(), func(c *tool.Call) { c.KnownURL = func(string) bool { return true } }), tool.PolicyConfirm, journal.DecisionConfirm},
		{"unknown policy fails closed", spec("x", tool.ClassRead, "weird"), base(), tool.PolicyForbidden, journal.DecisionForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol, dec := policy.Decide(policy.DefaultMaxDepth, tc.s, tc.c)
			if pol != tc.pol || dec != tc.dec {
				t.Fatalf("Decide = %s, %s; want %s, %s", pol, dec, tc.pol, tc.dec)
			}
		})
	}
}

// counter is a tool that counts how often it ran.
type counter struct {
	s     tool.Spec
	calls int
}

func (c *counter) Spec() tool.Spec { return c.s }
func (c *counter) Run(context.Context, tool.Call) (tool.Result, error) {
	c.calls++
	return tool.Result{Text: "ran"}, nil
}

type fakeAsker struct {
	asked  []string
	id     string
	err    error
	parked map[string]string // canonical arguments → the id of a call waiting with them
}

func (a *fakeAsker) Parked(_ context.Context, c tool.Call) (string, error) {
	key, _ := tool.CanonicalArgs(c.Args)
	return a.parked[c.Name+" "+key], nil
}

func (a *fakeAsker) Ask(_ context.Context, _ tool.Call, q string) (string, error) {
	a.asked = append(a.asked, q)
	return a.id, a.err
}

func (a *fakeAsker) Outgoing(_ context.Context, id string) (event.Outgoing, error) {
	return event.Outgoing{IdemKey: id + ":q", Text: a.asked[len(a.asked)-1], Buttons: []event.Button{{ID: "B", Label: "Approve"}}}, nil
}

// chain builds the production chain over h: journal-less, policy, registry.
func chain(t *testing.T, h *counter, ask policy.Asker, opts ...policy.Option) tool.Func {
	t.Helper()
	return chainCfg(t, policy.DefaultConfig(), h, ask, opts...)
}

func chainCfg(t *testing.T, cfg policy.Config, h *counter, ask policy.Asker, opts ...policy.Option) tool.Func {
	t.Helper()
	reg, err := tool.NewRegistry(router.Local{}, h)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Chain(reg.Run, policy.New(cfg, reg, ask, opts...).Middleware())
}

func TestConfirmDoesNotRun(t *testing.T) {
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	ask := &fakeAsker{id: "pend-1"}
	run := chain(t, h, ask)
	c := with(base(), func(c *tool.Call) {
		c.Name = "schedule.create"
		c.Args = args("token", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz")
	})
	res, err := run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if h.calls != 0 {
		t.Fatalf("the handler ran %d times", h.calls)
	}
	if res.Pending != "pend-1" || !strings.Contains(res.Text, "awaiting confirmation") || !strings.Contains(res.Text, "never that it is done") {
		t.Fatalf("result %+v", res)
	}
	// The question with its buttons goes out with the turn's replies.
	if len(res.Out) != 1 || res.Out[0].IdemKey != "pend-1:q" || len(res.Out[0].Buttons) != 1 {
		t.Fatalf("out %+v", res.Out)
	}
	if len(ask.asked) != 1 || !strings.Contains(ask.asked[0], "schedule.create") || strings.Contains(ask.asked[0], "abcdefghijkl") {
		t.Fatalf("question %q: want the tool and no secret", ask.asked)
	}
}

// questioner is a tool that words its own confirmation question.
type questioner struct {
	counter
	question string
	err      error
}

func (q *questioner) Question(_ context.Context, c tool.Call) (string, error) {
	return q.question + " (" + c.Name + ")", q.err
}

// A tool that words its own question is asked about in its words, with
// secrets still removed; a question it cannot word fails the call without
// asking.
func TestToolWordsItsQuestion(t *testing.T) {
	h := &questioner{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm),
		question: "Create a task?\nInstruction: use token=abcdefghijklmnopqrstuvwxyz"}
	ask := &fakeAsker{id: "pend-1"}
	reg, err := tool.NewRegistry(router.Local{}, h)
	if err != nil {
		t.Fatal(err)
	}
	run := tool.Chain(reg.Run, policy.New(policy.DefaultConfig(), reg, ask).Middleware())
	c := with(base(), func(c *tool.Call) { c.Name = "schedule.create" })
	if _, err := run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(ask.asked) != 1 || !strings.HasPrefix(ask.asked[0], "Create a task?\nInstruction: use token=") ||
		!strings.HasSuffix(ask.asked[0], "(schedule.create)") || strings.Contains(ask.asked[0], "abcdefghijkl") {
		t.Fatalf("asked %q", ask.asked)
	}
	h.err = errors.New("schedule.create: no such host")
	if _, err := run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "no such host") || len(ask.asked) != 1 || h.calls != 0 {
		t.Fatalf("an invalid call: err %v, asked %d times, ran %d", err, len(ask.asked), h.calls)
	}
}

func TestAskFailureFailsTheCall(t *testing.T) {
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	run := chain(t, h, policy.NoAsk{})
	_, err := run(context.Background(), with(base(), func(c *tool.Call) { c.Name = "schedule.create" }))
	if !errors.Is(err, policy.ErrNoAsker) || h.calls != 0 {
		t.Fatalf("err %v, ran %d times", err, h.calls)
	}
}

// An Asker that returns no id and no error has a bug: the call must not
// look parked.
func TestEmptyPendingIDFailsTheCall(t *testing.T) {
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	res, err := chain(t, h, &fakeAsker{})(context.Background(), with(base(), func(c *tool.Call) { c.Name = "schedule.create" }))
	if err == nil || res.Pending != "" || h.calls != 0 {
		t.Fatalf("result %+v, err %v, ran %d times", res, err, h.calls)
	}
}

func TestAutoRunsAndForbiddenDoesNot(t *testing.T) {
	h := &counter{s: spec("clock", tool.ClassRead, tool.PolicyAuto)}
	run := chain(t, h, &fakeAsker{})
	if res, err := run(context.Background(), with(base(), func(c *tool.Call) { c.Name = "clock" })); err != nil || res.Text != "ran" {
		t.Fatalf("auto call = %+v, %v", res, err)
	}
	_, err := run(context.Background(), with(base(), func(c *tool.Call) { c.Name = "clock"; c.Depth = 4 }))
	if !errors.Is(err, tool.ErrForbidden) || h.calls != 1 {
		t.Fatalf("deep call: err %v, ran %d times", err, h.calls)
	}
	if _, err := run(context.Background(), with(base(), func(c *tool.Call) { c.Name = "nope" })); !errors.Is(err, tool.ErrUnknown) {
		t.Fatalf("unknown tool: %v", err)
	}
}

func TestOverrideReplacesTheToolPolicy(t *testing.T) {
	h := &counter{s: spec("clock", tool.ClassRead, tool.PolicyAuto)}
	cfg := policy.DefaultConfig()
	cfg.Overrides = map[string]tool.Policy{"clock": tool.PolicyForbidden}
	_, err := chainCfg(t, cfg, h, &fakeAsker{})(context.Background(), with(base(), func(c *tool.Call) { c.Name = "clock" }))
	if !errors.Is(err, tool.ErrForbidden) || h.calls != 0 {
		t.Fatalf("err %v, ran %d times", err, h.calls)
	}
}

func TestMaxDepthIsConfigurable(t *testing.T) {
	h := &counter{s: spec("clock", tool.ClassRead, tool.PolicyAuto)}
	run := chainCfg(t, policy.Config{MaxDepth: 1}, h, &fakeAsker{})
	_, err := run(context.Background(), with(base(), func(c *tool.Call) { c.Name = "clock"; c.Depth = 2 }))
	if !errors.Is(err, tool.ErrForbidden) {
		t.Fatalf("depth 2 with max 1: %v", err)
	}
}

func TestDecisionReachesTheJournal(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	j, err := journal.New(ctx, db, time.Now, journal.DefaultRetain)
	if err != nil {
		t.Fatal(err)
	}
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	reg, _ := tool.NewRegistry(router.Local{}, h)
	ask := &fakeAsker{id: "p", parked: map[string]string{`schedule.create {"id":"dup"}`: "p0"}}
	run := tool.Chain(reg.Run, j.Middleware(), policy.New(policy.DefaultConfig(), reg, ask).Middleware())
	_, _ = run(ctx, with(base(), func(c *tool.Call) { c.Name = "schedule.create"; c.TurnID = "E1"; c.N = 1 }))
	// A call already waiting is journalled as confirm too (ruling R38).
	_, _ = run(ctx, with(base(), func(c *tool.Call) { c.Name = "schedule.create"; c.TurnID = "E2"; c.N = 1; c.Args = args("id", "dup") }))
	es, err := j.Entries(ctx)
	if err != nil || len(es) != 2 || es[0].Decision != journal.DecisionConfirm || es[1].Decision != journal.DecisionConfirm ||
		es[1].ResultHead != policy.AlreadyWaiting {
		t.Fatalf("entries %+v, %v", es, err)
	}
}

// grantAlerts is one admin with a private conversation.
func grantAlerts() alert.Admins {
	return alert.Admins{
		List:    func(context.Context) ([]string, error) { return []string{"telegram:user:1"}, nil },
		Private: func(p string) (string, string, bool) { return "telegram", "priv:" + p, true },
	}
}

func deniedRun(t *testing.T, run tool.Func, conv string) tool.Result {
	t.Helper()
	c := with(base(), func(c *tool.Call) {
		c.Name = "schedule.create"
		c.Conv = conv
		c.Grant = &tool.Grant{Tools: []string{"clock"}}
	})
	res, err := run(context.Background(), c)
	if !errors.Is(err, tool.ErrOutOfGrant) {
		t.Fatalf("err %v, want ErrOutOfGrant", err)
	}
	return res
}

func TestOutOfGrantAlertsOncePerDay(t *testing.T) {
	ctx := context.Background()
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	day := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	now := day
	run := chain(t, h, &fakeAsker{}, policy.WithAlerts(grantAlerts()), policy.WithClock(func() time.Time { return now }, time.UTC))

	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := sqlqueue.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	stored := func() int {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	var keys []string
	for range 3 {
		res := deniedRun(t, run, "task:J7")
		if len(res.Out) != 1 {
			t.Fatalf("Out = %+v, want one alert", res.Out)
		}
		keys = append(keys, res.Out[0].IdemKey)
		if err := q.Enqueue(ctx, res.Out...); err != nil {
			t.Fatal(err)
		}
	}
	const want = "alert:grant:J7:2026-11-03:telegram:user:1"
	if keys[0] != want || keys[1] != want || keys[2] != want {
		t.Fatalf("keys %v, want all %q", keys, want)
	}
	if n := stored(); n != 1 {
		t.Fatalf("%d outbox rows after three denied runs, want 1", n)
	}
	if h.calls != 0 {
		t.Fatalf("the tool ran %d times", h.calls)
	}

	now = day.Add(24 * time.Hour)
	res := deniedRun(t, run, "task:J7")
	if res.Out[0].IdemKey != "alert:grant:J7:2026-11-04:telegram:user:1" {
		t.Fatalf("next day key %q", res.Out[0].IdemKey)
	}
}

func TestOutOfGrantAlertUsesTheNodeDay(t *testing.T) {
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	at := time.Date(2026, 11, 3, 23, 30, 0, 0, time.UTC) // already Nov 4 in Kyiv
	run := chain(t, h, &fakeAsker{}, policy.WithAlerts(grantAlerts()), policy.WithClock(func() time.Time { return at }, kyiv))
	res := deniedRun(t, run, "not-a-task-conv")
	if got := res.Out[0].IdemKey; got != "alert:grant:not-a-task-conv:2026-11-04:telegram:user:1" {
		t.Fatalf("key %q: a conv without the task: prefix is used as it is", got)
	}
}

func TestOutOfGrantWithoutAlertsStillRefuses(t *testing.T) {
	h := &counter{s: spec("schedule.create", tool.ClassSideEffect, tool.PolicyConfirm)}
	res := deniedRun(t, chain(t, h, &fakeAsker{}), "task:J1")
	if len(res.Out) != 0 || h.calls != 0 {
		t.Fatalf("%+v, ran %d", res, h.calls)
	}
}

func TestUndeclaredArgumentsAreForbidden(t *testing.T) {
	h := &counter{s: spec("web.fetch", tool.ClassExternal, tool.PolicyAuto)}
	run := chain(t, h, &fakeAsker{id: "pend-1"})
	c := with(base(), func(c *tool.Call) {
		c.Name = "web.fetch"
		c.Args = json.RawMessage(`{"url":"https://known.example/","URL":"https://evil.example/?d=h"}`)
	})
	if _, err := run(context.Background(), c); !errors.Is(err, tool.ErrBadArgs) || h.calls != 0 {
		t.Fatalf("err %v, calls %d", err, h.calls)
	}
}

// A call identical to one already waiting for the owner in this
// conversation is not asked again: the model reads a fixed result, the
// call joins the waiting one's pending id, nothing runs and no second
// question goes out; it is journalled as confirm (ruling R38).
func TestWaitingCallIsNotAskedAgain(t *testing.T) {
	h := &counter{s: spec("web.fetch", tool.ClassExternal, tool.PolicyAuto)}
	ask := &fakeAsker{id: "pend-2", parked: map[string]string{`web.fetch {"url":"https://bank.example/eur"}`: "pend-1"}}
	run := chain(t, h, ask)
	same := with(base(), func(c *tool.Call) {
		c.Name = "web.fetch"
		c.Args = json.RawMessage(`{ "url": "https://bank.example/eur" }`)
	})
	res, err := run(context.Background(), same)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != "pend-1" || res.Text != policy.AlreadyWaiting || len(res.Out) != 0 || len(ask.asked) != 0 || h.calls != 0 {
		t.Fatalf("the same call: result %+v, asked %d, ran %d", res, len(ask.asked), h.calls)
	}
	other := with(same, func(c *tool.Call) { c.Args = args("url", "https://bank.example/usd") })
	res, err = run(context.Background(), other)
	if err != nil || res.Pending != "pend-2" || len(res.Out) != 1 || len(ask.asked) != 1 {
		t.Fatalf("another url: result %+v, err %v, asked %d", res, err, len(ask.asked))
	}
}

// ADR-0049: a read script may reach the network, so in a tainted session
// the session's data must not reach its input unasked.
func TestTaintedInputAsks(t *testing.T) {
	script := spec("skill.report", tool.ClassExternal, tool.PolicyAuto)
	script.AskTaintedInput = true
	tainted := func(a string) tool.Call {
		return with(base(), func(c *tool.Call) { c.Tainted, c.Args = true, json.RawMessage(a) })
	}
	grant := tainted(`{"input":"data"}`)
	grant.Grant = &tool.Grant{Tools: []string{"skill.report"}}
	plain := spec("web.search", tool.ClassExternal, tool.PolicyAuto)
	for _, tc := range []struct {
		name string
		s    tool.Spec
		c    tool.Call
		pol  tool.Policy
	}{
		{"input in a tainted session", script, tainted(`{"input":"data"}`), tool.PolicyConfirm},
		{"no input", script, tainted(`{}`), tool.PolicyAuto},
		{"empty input", script, tainted(`{"input":""}`), tool.PolicyAuto},
		{"input that is not a string", script, tainted(`{"input":5}`), tool.PolicyConfirm},
		{"null input", script, tainted(`{"input":null}`), tool.PolicyConfirm},
		{"arguments that do not parse", script, tainted(`{"input"`), tool.PolicyConfirm},
		{"clean session", script, with(base(), func(c *tool.Call) { c.Args = json.RawMessage(`{"input":"data"}`) }), tool.PolicyAuto},
		{"inside a grant", script, grant, tool.PolicyAuto},
		{"a tool that does not ask", plain, tainted(`{"input":"data"}`), tool.PolicyAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if pol, _ := policy.Decide(policy.DefaultMaxDepth, tc.s, tc.c); pol != tc.pol {
				t.Fatalf("Decide = %s, want %s", pol, tc.pol)
			}
		})
	}
}

// ADR-0049: a local-only session keeps its tools on the node; a grant does
// not change that, and a tool that does not say it is safe is refused.
func TestLocalOnlyRefusesATool(t *testing.T) {
	leaves := spec("web.search", tool.ClassExternal, tool.PolicyAuto)
	safe := spec("cells.get", tool.ClassRead, tool.PolicyAuto)
	safe.LocalSafe = true
	local := with(base(), func(c *tool.Call) { c.LocalOnly = true })
	granted := with(local, func(c *tool.Call) { c.Grant = &tool.Grant{Tools: []string{"web.search"}} })
	for _, tc := range []struct {
		name string
		s    tool.Spec
		c    tool.Call
		pol  tool.Policy
		dec  journal.Decision
	}{
		{"a tool that leaves", leaves, local, tool.PolicyForbidden, journal.DecisionForbidden},
		{"inside a grant", leaves, granted, tool.PolicyForbidden, journal.DecisionForbidden},
		{"a safe tool", safe, local, tool.PolicyAuto, journal.DecisionAllowed},
		{"another session", leaves, base(), tool.PolicyAuto, journal.DecisionAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if pol, dec := policy.Decide(policy.DefaultMaxDepth, tc.s, tc.c); pol != tc.pol || dec != tc.dec {
				t.Fatalf("Decide = %s, %s; want %s, %s", pol, dec, tc.pol, tc.dec)
			}
		})
	}
}

// The model reads why, so it can tell the owner instead of trying again.
func TestLocalOnlyRefusalSaysWhy(t *testing.T) {
	h := &counter{s: spec("web.search", tool.ClassExternal, tool.PolicyAuto)}
	_, err := chain(t, h, policy.NoAsk{})(context.Background(), with(base(), func(c *tool.Call) {
		c.LocalOnly, c.Name = true, "web.search"
	}))
	if !errors.Is(err, tool.ErrForbidden) || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("err %v", err)
	}
	if h.calls != 0 {
		t.Fatal("the tool ran")
	}
}

func execSpec() tool.Spec {
	return tool.Spec{Name: "host.exec", Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true}
}

func execCall(argv ...string) tool.Call {
	args, _ := json.Marshal(map[string]any{"argv": argv, "reason": "r"})
	return tool.Call{Name: "host.exec", Admin: true, Args: args}
}

// R-CMD-2, R-CMD-7: a host command is decided by its class in the table, and
// a forbidden one never runs, whatever else holds.
func TestDecideHostExec(t *testing.T) {
	planned := func(cmd tool.Command) bool { return cmd.Equal(tool.Command{Argv: []string{"go", "clean", "-cache"}}) }
	cases := []struct {
		name string
		call tool.Call
		pol  tool.Policy
		d    journal.Decision
	}{
		{"read runs", execCall("df", "-h"), tool.PolicyAuto, journal.DecisionAllowed},
		{"read runs tainted", func() tool.Call { c := execCall("du", "-sh", "/x"); c.Tainted = true; return c }(), tool.PolicyAuto, journal.DecisionAllowed},
		{"content asks", execCall("cat", "/etc/hosts"), tool.PolicyConfirm, journal.DecisionConfirm},
		{"content asks tainted", func() tool.Call { c := execCall("cat", "/etc/hosts"); c.Tainted = true; return c }(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"change asks", execCall("rm", "-r", "/tmp/x"), tool.PolicyConfirm, journal.DecisionConfirm},
		{"script asks", execCall("bash", "-c", "true"), tool.PolicyConfirm, journal.DecisionConfirm},
		{"forbidden never", execCall("sudo", "true"), tool.PolicyForbidden, journal.DecisionForbidden},
		{"planned runs", func() tool.Call { c := execCall("go", "clean", "-cache"); c.Planned = planned; return c }(), tool.PolicyAuto, journal.DecisionPlanned},
		{"planned elsewhere asks", func() tool.Call {
			args, _ := json.Marshal(map[string]any{"argv": []string{"go", "clean", "-cache"}, "dir": "/other", "reason": "r"})
			c := execCall()
			c.Args, c.Planned = args, planned
			return c
		}(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"not in the plan asks", func() tool.Call { c := execCall("docker", "rm", "x"); c.Planned = planned; return c }(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"forbidden though planned", func() tool.Call {
			c := execCall("sudo", "true")
			c.Planned = func(tool.Command) bool { return true }
			return c
		}(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"forbidden though granted", func() tool.Call {
			c := execCall("sudo", "true")
			c.Grant = &tool.Grant{Tools: []string{"host.exec"}}
			return c
		}(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"unparsed argv is forbidden", tool.Call{Name: "host.exec", Admin: true, Args: json.RawMessage(`{"argv":"rm -rf /","reason":"r"}`)}, tool.PolicyForbidden, journal.DecisionForbidden},
		{"empty argv is forbidden", execCall(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"not for a non-admin", func() tool.Call { c := execCall("df"); c.Admin = false; return c }(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"not in a local-only session", func() tool.Call { c := execCall("df"); c.LocalOnly = true; return c }(), tool.PolicyForbidden, journal.DecisionForbidden},
	}
	for _, c := range cases {
		pol, d := policy.Decide(policy.DefaultMaxDepth, execSpec(), c.call)
		if pol != c.pol || d != c.d {
			t.Errorf("%s: %s %s, want %s %s", c.name, pol, d, c.pol, c.d)
		}
	}
}

func planSpec() tool.Spec {
	return tool.Spec{Name: "plan.show", Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true, KeepsQuestion: true}
}

func planCall(steps ...any) tool.Call {
	args, _ := json.Marshal(map[string]any{"steps": steps})
	return tool.Call{Name: "plan.show", Admin: true, Args: args}
}

func planStep(argv ...string) map[string]any {
	return map[string]any{"text": "step", "argv": argv, "reason": "r"}
}

// R-CMD-7: a plan with a command that needs a press, which the
// conversation's approved plan does not list, is asked as one question; a
// forbidden command makes the whole plan forbidden.
func TestDecidePlanShow(t *testing.T) {
	planned := func(cmd tool.Command) bool { return cmd.Equal(tool.Command{Argv: []string{"go", "clean", "-cache"}}) }
	cases := []struct {
		name string
		call tool.Call
		pol  tool.Policy
		d    journal.Decision
	}{
		{"words only run", planCall("a", "b"), tool.PolicyAuto, journal.DecisionAllowed},
		{"a read command runs", planCall("a", planStep("df", "-h")), tool.PolicyAuto, journal.DecisionAllowed},
		{"a read command runs tainted", func() tool.Call { c := planCall(planStep("df", "-h")); c.Tainted = true; return c }(),
			tool.PolicyAuto, journal.DecisionAllowed},
		{"a change asks", planCall("a", planStep("go", "clean", "-cache")), tool.PolicyConfirm, journal.DecisionConfirm},
		{"a content command asks", planCall(planStep("cat", "/etc/hosts")), tool.PolicyConfirm, journal.DecisionConfirm},
		{"planned runs", func() tool.Call { c := planCall(planStep("go", "clean", "-cache")); c.Planned = planned; return c }(),
			tool.PolicyAuto, journal.DecisionAllowed},
		{"planned and another asks", func() tool.Call {
			c := planCall(planStep("go", "clean", "-cache"), planStep("cargo", "clean"))
			c.Planned = planned
			return c
		}(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"planned elsewhere asks", func() tool.Call {
			c := planCall(map[string]any{"text": "x", "argv": []string{"go", "clean", "-cache"}, "dir": "/other"})
			c.Planned = planned
			return c
		}(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"docker run is forbidden", planCall("a", planStep("docker", "run", "x")), tool.PolicyForbidden, journal.DecisionForbidden},
		{"forbidden though planned", func() tool.Call {
			c := planCall(planStep("sudo", "true"))
			c.Planned = func(tool.Command) bool { return true }
			return c
		}(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"steps that do not parse ask", tool.Call{Name: "plan.show", Admin: true, Args: json.RawMessage(`{"steps":[{"text":"x","argv":"rm -rf /"}]}`)},
			tool.PolicyConfirm, journal.DecisionConfirm},
		{"not in a local-only session", func() tool.Call { c := planCall("a"); c.LocalOnly = true; return c }(), tool.PolicyForbidden, journal.DecisionForbidden},
	}
	for _, c := range cases {
		pol, d := policy.Decide(policy.DefaultMaxDepth, planSpec(), c.call)
		if pol != c.pol || d != c.d {
			t.Errorf("%s: %s %s, want %s %s", c.name, pol, d, c.pol, c.d)
		}
	}
}

// A plan in a scheduled run is decided by the grant (the tool then refuses
// it: a task's conversation is no chat, errPlanInTask), but a forbidden
// command in it is forbidden before any grant (review focus 2).
func TestPlanInATask(t *testing.T) {
	inGrant := &tool.Grant{Tools: []string{"plan.show"}}
	for _, tc := range []struct {
		name  string
		grant *tool.Grant
		step  map[string]any
		pol   tool.Policy
		d     journal.Decision
	}{
		{"outside the grant", &tool.Grant{Tools: []string{"web.search"}}, planStep("df", "-h"), tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{"inside the grant", inGrant, planStep("go", "clean", "-cache"), tool.PolicyAuto, journal.DecisionAllowed},
		{"forbidden though granted", inGrant, planStep("sudo", "true"), tool.PolicyForbidden, journal.DecisionForbidden},
	} {
		c := planCall(tc.step)
		c.Grant = tc.grant
		if pol, d := policy.Decide(policy.DefaultMaxDepth, planSpec(), c); pol != tc.pol || d != tc.d {
			t.Errorf("%s: %s %s, want %s %s", tc.name, pol, d, tc.pol, tc.d)
		}
	}
}

// R-POL-8: a task runs a read command whenever host.exec is granted and any
// other only as listed, argv exact; a forbidden one never runs.
func TestCommandsInAGrant(t *testing.T) {
	g := &tool.Grant{Tools: []string{"host.exec"}, Commands: []tool.Command{{Argv: []string{"docker", "image", "prune", "-af"}}}}
	cases := []struct {
		argv []string
		pol  tool.Policy
		d    journal.Decision
	}{
		{[]string{"docker", "image", "prune", "-af"}, tool.PolicyAuto, journal.DecisionAllowed},
		{[]string{"docker", "system", "df"}, tool.PolicyAuto, journal.DecisionAllowed}, // read needs no listing
		{[]string{"docker", "rm", "x"}, tool.PolicyForbidden, journal.DecisionOutOfGrant},
		{[]string{"docker", "image", "prune", "-a"}, tool.PolicyForbidden, journal.DecisionOutOfGrant}, // not the same argv
		{[]string{"docker", "run", "x"}, tool.PolicyForbidden, journal.DecisionForbidden},
	}
	for _, c := range cases {
		call := execCall(c.argv...)
		call.Grant = g
		call.Tainted = true // a run's own reads taint it; the grant still holds (R-POL-8)
		if pol, d := policy.Decide(policy.DefaultMaxDepth, execSpec(), call); pol != c.pol || d != c.d {
			t.Errorf("%v: %s %s, want %s %s", c.argv, pol, d, c.pol, c.d)
		}
	}
}

// The folder is part of the command: the same argv elsewhere is not granted.
func TestACommandInAnotherFolderIsOutsideTheGrant(t *testing.T) {
	g := &tool.Grant{Tools: []string{"host.exec"}, Commands: []tool.Command{{Argv: []string{"rm", "-r", "build"}, Dir: "/srv/a"}}}
	for dir, want := range map[string]journal.Decision{"/srv/a": journal.DecisionAllowed, "/srv/b": journal.DecisionOutOfGrant, "": journal.DecisionOutOfGrant} {
		args, _ := json.Marshal(map[string]any{"argv": []string{"rm", "-r", "build"}, "dir": dir, "reason": "r"})
		call := tool.Call{Name: "host.exec", Admin: true, Args: args, Grant: g}
		if _, d := policy.Decide(policy.DefaultMaxDepth, execSpec(), call); d != want {
			t.Errorf("dir %q: %s, want %s", dir, d, want)
		}
	}
}

// R-CMD-2: a forbidden command never runs, whatever the grant lists.
func TestForbiddenCommandBeatsAGrant(t *testing.T) {
	g := &tool.Grant{Tools: []string{"host.exec"}, Commands: []tool.Command{{Argv: []string{"sudo", "true"}}, {Argv: []string{"docker", "run", "x"}}}}
	for _, argv := range [][]string{{"sudo", "true"}, {"docker", "run", "x"}} {
		call := execCall(argv...)
		call.Grant = g
		if pol, d := policy.Decide(policy.DefaultMaxDepth, execSpec(), call); pol != tool.PolicyForbidden || d != journal.DecisionForbidden {
			t.Errorf("%v: %s %s", argv, pol, d)
		}
	}
}

// R-POL-8: a command outside the grant is refused, the alert names it, and a
// second call the same day makes the same notice.
func TestACommandOutsideTheGrantAlertsOnceAndNamesIt(t *testing.T) {
	s := execSpec()
	s.InputSchema = json.RawMessage(`{"type":"object","properties":{"argv":{"type":"array"},"reason":{"type":"string"}}}`)
	h := &counter{s: s}
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	run := chain(t, h, &fakeAsker{}, policy.WithAlerts(grantAlerts()), policy.WithClock(func() time.Time { return now }, time.UTC))
	var keys []string
	for _, argv := range [][]string{{"rm", "-r", "x"}, {"mv", "a", "b"}} {
		c := execCall(argv...)
		c.Conv = "task:J7"
		c.Grant = &tool.Grant{Tools: []string{"host.exec"}}
		res, err := run(context.Background(), c)
		if !errors.Is(err, tool.ErrOutOfGrant) || len(res.Out) != 1 {
			t.Fatalf("%v: %+v, %v", argv, res, err)
		}
		if argv[0] == "rm" && !strings.Contains(res.Out[0].Text, "rm -r x") {
			t.Errorf("the alert does not name the command: %q", res.Out[0].Text)
		}
		keys = append(keys, res.Out[0].IdemKey)
	}
	if keys[0] != keys[1] || keys[0] != "alert:grant:J7:2026-11-03:telegram:user:1" {
		t.Fatalf("keys %v", keys)
	}
	if h.calls != 0 {
		t.Fatalf("the tool ran %d times", h.calls)
	}
}
