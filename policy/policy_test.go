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
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
)

func spec(name string, class tool.Class, pol tool.Policy) tool.Spec {
	return tool.Spec{Name: name, Class: class, Policy: pol, AdminOnly: true}
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
		{"grant covers a side effect in a tainted session (R-POL-8)", side, with(base(), func(c *tool.Call) { c.Grant = grant; c.Tainted = true }), tool.PolicyAuto, journal.DecisionAllowed},
		{"grant naming a forbidden tool is still forbidden", denied, with(base(), func(c *tool.Call) { c.Grant = &tool.Grant{Tools: []string{"danger"}} }), tool.PolicyForbidden, journal.DecisionForbidden},
		{"memory forget, clean, is auto", forget, base(), tool.PolicyAuto, journal.DecisionAllowed},
		{"forbidden tool", denied, base(), tool.PolicyForbidden, journal.DecisionForbidden},
		{"side effect confirms", side, base(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"side effect confirms even if the tool says auto", sideAuto, base(), tool.PolicyConfirm, journal.DecisionConfirm},
		{"memory write, clean, is auto", memory, base(), tool.PolicyAuto, journal.DecisionAllowed},
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
	asked []string
	id    string
	err   error
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
	reg, err := tool.NewRegistry(h)
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
	if res.Pending != "pend-1" || !strings.Contains(res.Text, "awaiting confirmation") {
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
	reg, _ := tool.NewRegistry(h)
	run := tool.Chain(reg.Run, j.Middleware(), policy.New(policy.DefaultConfig(), reg, &fakeAsker{id: "p"}).Middleware())
	_, _ = run(ctx, with(base(), func(c *tool.Call) { c.Name = "schedule.create"; c.TurnID = "E1"; c.N = 1 }))
	es, err := j.Entries(ctx)
	if err != nil || len(es) != 1 || es[0].Decision != journal.DecisionConfirm {
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
