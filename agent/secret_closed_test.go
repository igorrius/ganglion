package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
)

// secretEvent is the event the node's intake puts for a closed secret request.
func secretEvent(s event.SecretClosed) event.Event {
	seq++
	p, _ := json.Marshal(s)
	return event.Event{ID: fmt.Sprintf("01SC%04d", seq), IdemKey: fmt.Sprintf("intake:%s:closed:%d", s.Request, seq),
		Source: event.SourceIntake, Conv: "telegram:42", Kind: event.KindSecretClosed, Payload: p, At: t0}
}

func secretClosedOf(o event.SecretOutcome) event.SecretClosed {
	return event.SecretClosed{Request: "req1", Name: "WIFI_PASSWORD", Scope: "module:local/demo", Outcome: o,
		Principal: owner, Transport: "telegram", At: t0.Add(time.Hour)}
}

// R-INT-6: a closed secret request becomes one node note in the session,
// with no model call and no outgoing message; the three outcomes read
// differently and none holds a value.
func TestSecretClosedWritesANodeNote(t *testing.T) {
	for _, tc := range []struct {
		out  event.SecretOutcome
		want string
	}{
		{event.SecretSet, "was set by the owner"},
		{event.SecretExpired, "expired"},
		{event.SecretFailed, "was not set"},
	} {
		t.Run(string(tc.out), func(t *testing.T) {
			e := newEnv(t, Config{}, nil)
			if out := e.turn(t, secretEvent(secretClosedOf(tc.out))); len(out) != 0 {
				t.Fatalf("a secret note sent %+v", out)
			}
			if n := len(e.smart.requests()) + len(e.cheap.requests()); n != 0 {
				t.Fatalf("%d model calls", n)
			}
			st := e.state(t)
			if len(st.Messages) != 1 {
				t.Fatalf("history %+v", st.Messages)
			}
			got := st.Messages[0].Content
			for _, w := range []string{"[secret] ", "WIFI_PASSWORD", "module:local/demo", tc.want} {
				if !strings.Contains(got, w) {
					t.Errorf("the note %q lacks %q", got, w)
				}
			}
			if !strings.HasPrefix(got, "[secret] ") || st.Tainted {
				t.Errorf("note %q, tainted %v", got, st.Tainted)
			}
		})
	}
}

// Abuse: a secret note that is not the node's, or is for someone who is not
// an admin, writes nothing.
func TestSecretClosedFromElsewhereIsDropped(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	forged := secretEvent(secretClosedOf(event.SecretSet))
	forged.Source = "telegram"
	if out := e.turn(t, forged); len(out) != 0 {
		t.Fatalf("a forged note sent %+v", out)
	}
	if st := e.state(t); len(st.Messages) != 0 {
		t.Fatalf("a forged note left history %+v", st.Messages)
	}
	e.admin = false
	if out := e.turn(t, secretEvent(secretClosedOf(event.SecretSet))); len(out) != 0 {
		t.Fatalf("a note for a non-admin sent %+v", out)
	}
	if st := e.state(t); len(st.Messages) != 0 {
		t.Fatalf("a note for a non-admin left history %+v", st.Messages)
	}
	if n := len(e.smart.requests()); n != 0 {
		t.Fatalf("%d model calls", n)
	}
}
