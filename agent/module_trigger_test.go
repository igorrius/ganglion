package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
)

// legacyTrigger is a module.trigger event left in the inbox from before the
// upgrade.
func legacyTrigger() event.Event {
	seq++
	return event.Event{ID: "01TR" + strings.Repeat("0", 3) + string(rune('A'+seq%26)), IdemKey: "trigger:local/demo-alerts:local/demo-reader:r",
		Source: event.SourceModule, Conv: "module:demo-alerts", Kind: event.KindModuleTrigger, At: t0,
		Payload: json.RawMessage(`{"module":"local/demo-alerts","producer":"local/demo-reader","run":"r","cells":["demo-reader.soc"],"depth":1}`)}
}

// A module.trigger event from before the upgrade is acknowledged and
// dropped with one info line: no model, no message, no state, no turn row.
// Cell triggers travel the bus now (R-RUN-15, ADR-0108).
func TestALegacyModuleTriggerIsDropped(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	never := func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		t.Error("a model was called for a module trigger")
		return provider.ChatResponse{}, errors.New("no model")
	}
	e.smart.push(never)
	e.cheap.push(never)
	ev := legacyTrigger()
	out, st, err := e.a.Handle(context.Background(), ev)
	if err != nil || len(out) != 0 || st.Conv != "" || st.Data != nil || len(st.Taken) != 0 {
		t.Fatalf("Handle = %+v, %+v, %v", out, st, err)
	}
	if n := len(e.smart.requests()) + len(e.cheap.requests()); n != 0 {
		t.Fatalf("%d model calls", n)
	}
	if _, err := e.turns.Get(context.Background(), ev.ID); err == nil {
		t.Fatal("a module trigger left a turn")
	}
	if n := strings.Count(e.logs.String(), "left in the inbox from before the upgrade"); n != 1 {
		t.Fatalf("%d log lines:\n%s", n, e.logs.String())
	}
}
