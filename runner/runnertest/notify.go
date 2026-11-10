package runnertest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/igorrius/ganglion/runner"
)

// fakeNotify is a runner.NotifyPort that records the texts and answers with
// a configured code.
type fakeNotify struct {
	mu    sync.Mutex
	code  runner.NotifyCode
	texts []string
}

func (f *fakeNotify) Notify(_ context.Context, text string) runner.NotifyCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return f.code
}

func (f *fakeNotify) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func runNotifyTests(t *testing.T, fresh func() *env) {
	grant := func(e *env) { e.manifest.Capabilities.Notify = &runner.Notify{PerDay: 5} }

	t.Run("notify reaches the port with the text", func(t *testing.T) {
		e := fresh()
		grant(e)
		r := result(t, call(t, e.instance(t), `{"op":"notify","value":"battery low"}`))
		if r["err"] != "" {
			t.Fatalf("notify = %v, want sent", r)
		}
		if got := e.notify.sent(); len(got) != 1 || got[0] != "battery low" {
			t.Fatalf("port saw %q, want [battery low]", got)
		}
	})

	t.Run("notify without the grant is denied and the port sees nothing", func(t *testing.T) {
		e := fresh()
		r := result(t, call(t, e.instance(t), `{"op":"notify","value":"hi"}`))
		if r["err"] != "denied" {
			t.Fatalf("notify = %v, want denied", r)
		}
		if got := e.notify.sent(); len(got) != 0 {
			t.Fatalf("port saw %q, want nothing", got)
		}
	})

	t.Run("a long text arrives cut at 1000 characters", func(t *testing.T) {
		e := fresh()
		grant(e)
		long := strings.Repeat("x", 2000)
		result(t, call(t, e.instance(t), `{"op":"notify","value":"`+long+`"}`))
		got := e.notify.sent()
		if len(got) != 1 || got[0] != strings.Repeat("x", 999)+"…" {
			t.Fatalf("port saw %d texts, first of %d bytes; want one of 999 x and an ellipsis", len(got), len(got[0]))
		}
	})

	for name, tc := range map[string]struct {
		code runner.NotifyCode
		want string
	}{
		"limit":    {runner.NotifyLimit, "limit"},
		"not here": {runner.NotifyNotHere, "not_here"},
		"denied":   {runner.NotifyDenied, "denied"},
	} {
		t.Run("the guest maps the port's code: "+name, func(t *testing.T) {
			e := fresh()
			grant(e)
			e.notify.code = tc.code
			r := result(t, call(t, e.instance(t), `{"op":"notify","value":"x"}`))
			if r["err"] != tc.want {
				t.Fatalf("notify = %v, want err %q", r, tc.want)
			}
		})
	}
}
