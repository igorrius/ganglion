package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modstore"
)

// fakeModules records what /modules asked for.
type fakeModules struct {
	calls []string
}

func (f *fakeModules) List(context.Context) ([]modstore.Version, error) {
	f.calls = append(f.calls, "list")
	return []modstore.Version{
		{Module: "html2md", Version: "0.2.0", Source: "html2md/v0.2.0", State: modstore.StateActive},
		{Module: "html2md", Version: "builtin", Source: modstore.SourceBuiltin, State: modstore.StatePrevious},
		{Module: "html2md", Version: "0.3.0", Source: "html2md/v0.3.0", State: modstore.StateFailed, Reason: "the self-test failed: x"},
	}, nil
}

func (f *fakeModules) Update(_ context.Context, name string) (modstore.Outcome, error) {
	f.calls = append(f.calls, "update "+name)
	if name != "html2md" {
		return modstore.Outcome{}, modstore.ErrUnknownModule
	}
	return modstore.Outcome{Module: name, From: "builtin", To: "0.3.0", Result: modstore.ResultFailedSelfTest, Reason: "the self-test failed: trap"}, nil
}

func (f *fakeModules) Rollback(_ context.Context, name string) (modstore.Outcome, error) {
	f.calls = append(f.calls, "rollback "+name)
	return modstore.Outcome{Module: name, From: "0.2.0", To: "builtin", Result: modstore.ResultRolledBack}, nil
}

func modulesEnv(t *testing.T) (*env, *fakeModules) {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	m := &fakeModules{}
	e.deps.Modules = m
	e.a = New(overDefaults(Config{}), e.deps)
	return e, m
}

// R-RUN-8: /modules lists, updates and rolls back, without a model, and
// says in plain words why an update was refused.
func TestModulesCommand(t *testing.T) {
	e, m := modulesEnv(t)
	for _, c := range [][2]string{
		{"/modules", "html2md: 0.2.0 (html2md/v0.2.0); previous: builtin; 0.3.0 failed: the self-test failed: x"},
		{"/modules update html2md", "html2md stays on builtin: the self-test failed: trap"},
		{"/modules rollback html2md", "html2md rolled back from 0.2.0 to builtin."},
		{"/modules update deye", "No module named deye."},
		{"/modules frobnicate", "Usage: /modules, /modules update <name>, /modules rollback <name>"},
	} {
		text, want := c[0], c[1]
		out := e.turn(t, message(owner, "Igor", text))
		if len(out) != 1 || !strings.Contains(out[0].Text, want) {
			t.Errorf("%s: out %+v, want %q", text, out, want)
		}
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
	if got := strings.Join(m.calls, ","); got != "list,update html2md,rollback html2md,update deye" {
		t.Fatalf("calls %s", got)
	}
}

func TestModulesCommandAdminOnly(t *testing.T) {
	e, m := modulesEnv(t)
	e.admin = false
	out := e.turn(t, message(owner, "Igor", "/modules update html2md"))
	if len(out) != 1 || out[0].Text != replyTasksNotAvailable || len(m.calls) != 0 {
		t.Fatalf("out %+v, calls %v", out, m.calls)
	}
}
