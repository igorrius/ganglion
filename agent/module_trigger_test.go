package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
)

// triggerEvent is the event the module host puts for a listener.
func triggerEvent(m event.ModuleTrigger) event.Event {
	seq++
	p, _ := json.Marshal(m)
	return event.Event{ID: fmt.Sprintf("01TR%04d", seq), IdemKey: "trigger:" + m.Module + ":" + m.Producer + ":" + m.Run,
		Source: event.SourceModule, Conv: "module:" + strings.TrimPrefix(m.Module, "local/"), Kind: event.KindModuleTrigger, Payload: p, At: t0}
}

func alertsTrigger() event.ModuleTrigger {
	return event.ModuleTrigger{Module: "local/deye-alerts", Producer: "local/deye-reader", Run: "cron:module:local/deye-reader:0:1",
		Cells: []string{"deye-reader.grid", "deye-reader.soc"}, Depth: 1}
}

// noModel makes both test providers fail the test if a model is called.
func (e *env) noModel(t *testing.T) {
	t.Helper()
	never := func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		t.Error("a model was called for a module trigger")
		return provider.ChatResponse{}, errors.New("no model")
	}
	e.smart.push(never)
	e.cheap.push(never)
}

// triggerPort records the listener runs the agent asked for.
type triggerPort struct {
	got []event.ModuleTrigger
	ids []string
	err error
}

func (p *triggerPort) run(_ context.Context, m event.ModuleTrigger, id string) error {
	p.got, p.ids = append(p.got, m), append(p.ids, id)
	return p.err
}

func triggerEnv(t *testing.T) (*env, *triggerPort) {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	e.noModel(t)
	p := &triggerPort{}
	e.deps.ModuleTrigger = p.run
	e.a = New(overDefaults(Config{}), e.deps)
	return e, p
}

// A module.trigger event runs the listener through the module host and is
// no turn: no model, no message, no state, no turn row (R-RUN-15, ruling 4).
func TestAModuleTriggerRunsTheListenerWithNoTurn(t *testing.T) {
	e, p := triggerEnv(t)
	ev := triggerEvent(alertsTrigger())
	out, st, err := e.a.Handle(context.Background(), ev)
	if err != nil || len(out) != 0 || st.Conv != "" || st.Data != nil || len(st.Taken) != 0 {
		t.Fatalf("Handle = %+v, %+v, %v", out, st, err)
	}
	want := alertsTrigger()
	if len(p.got) != 1 || p.ids[0] != ev.ID || p.got[0].Module != want.Module || p.got[0].Producer != want.Producer ||
		p.got[0].Run != want.Run || !slices.Equal(p.got[0].Cells, want.Cells) || p.got[0].Depth != 1 {
		t.Fatalf("the host ran %+v %v", p.got, p.ids)
	}
	if n := len(e.smart.requests()) + len(e.cheap.requests()); n != 0 {
		t.Fatalf("%d model calls", n)
	}
	if _, err := e.turns.Get(context.Background(), ev.ID); err == nil {
		t.Fatal("a module trigger left a turn")
	}
}

// When the host cannot start the run, the event fails and the queue tries
// it again.
func TestAModuleTriggerTheHostCannotStartIsRetried(t *testing.T) {
	e, p := triggerEnv(t)
	p.err = context.Canceled
	if _, _, err := e.a.Handle(context.Background(), triggerEvent(alertsTrigger())); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle = %v, want the host's error", err)
	}
}

// A trigger the node did not put, or one it cannot read, runs nothing and
// is acknowledged: a transport cannot start a module (abuse).
func TestAForeignOrBrokenTriggerRunsNothing(t *testing.T) {
	e, p := triggerEnv(t)
	foreign := triggerEvent(alertsTrigger())
	foreign.Source = "telegram"
	broken := triggerEvent(alertsTrigger())
	broken.Payload = json.RawMessage(`{"module":`)
	empty := triggerEvent(event.ModuleTrigger{Producer: "local/deye-reader", Run: "x", Cells: []string{"a.b"}})
	for _, ev := range []event.Event{foreign, broken, empty} {
		out, st, err := e.a.Handle(context.Background(), ev)
		if err != nil || len(out) != 0 || st.Conv != "" {
			t.Fatalf("Handle(%s) = %+v, %+v, %v", ev.Payload, out, st, err)
		}
	}
	if len(p.got) != 0 {
		t.Fatalf("the host ran %+v", p.got)
	}
	if n := strings.Count(e.logs.String(), "agent: a module trigger"); n != 3 {
		t.Fatalf("%d warnings:\n%s", n, e.logs.String())
	}
}
