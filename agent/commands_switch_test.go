package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSwitch is a kill switch in memory.
type fakeSwitch struct {
	on  bool
	by  string
	set int
}

func (f *fakeSwitch) On(context.Context) bool { return f.on }
func (f *fakeSwitch) Set(_ context.Context, on bool, by string) error {
	f.on, f.by = on, by
	f.set++
	return nil
}

func switchEnv(t *testing.T) (*env, *fakeSwitch) {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	s := &fakeSwitch{on: true}
	e.deps.Switch = s
	e.a = New(overDefaults(Config{}), e.deps)
	return e, s
}

// R-POL-5: an admin's /commands turns host commands off and on, without a model.
func TestCommandsCommand(t *testing.T) {
	e, s := switchEnv(t)
	for _, c := range [][2]string{
		{"/commands off", "Commands are off. /commands on turns them back on."},
		{"/commands", "Commands are off."},
		{"/commands on", "Commands are on."},
		{"/commands", "Write /commands on or /commands off."},
		{"/commands maybe", "Write /commands on or /commands off."},
	} {
		out := e.turn(t, message(owner, "Igor", c[0]))
		if len(out) != 1 || !strings.Contains(out[0].Text, c[1]) {
			t.Errorf("%s: out %+v, want %q", c[0], out, c[1])
		}
	}
	if s.set != 2 || s.by == "" {
		t.Fatalf("set %d times by %q", s.set, s.by)
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
}

func TestCommandsCommandAdminOnly(t *testing.T) {
	e, s := switchEnv(t)
	e.admin = false
	out := e.turn(t, message(owner, "Igor", "/commands off"))
	if len(out) != 1 || out[0].Text != replyTasksNotAvailable || s.set != 0 || !s.on {
		t.Fatalf("out %+v, switch %+v", out, s)
	}
}

func TestNoSwitchRefusesSet(t *testing.T) {
	if (NoSwitch{}).On(context.Background()) {
		t.Fatal("NoSwitch is on")
	}
	if err := (NoSwitch{}).Set(context.Background(), true, "x"); !errors.Is(err, ErrNoSwitch) {
		t.Fatalf("err %v", err)
	}
}
