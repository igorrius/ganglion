package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
)

// linkedChainRun is a run of demo-alerts that a call of demo-reader's run
// started, itself started by an event over a link.
func linkedChainRun(eventLink, callLink string) modstore.Run {
	reader := readerHop()
	ev := modstore.Hop{Module: "local/demo-bridge", Version: "2", Trigger: "event", Link: eventLink, At: runAt, Took: 100 * time.Millisecond, Result: "ok"}
	call := modstore.Hop{Module: "local/demo-alerts", Version: "1", Trigger: "call", Link: callLink, At: runAt, Result: "ok"}
	return modstore.Run{Hop: call, ID: "r3", Producer: "local/demo-bridge", ProducerRun: "r2", Depth: 2, Chain: []modstore.Hop{reader, ev}}
}

// R-RUN-20: /why names the link an event or a call run came over, in the
// node's words; the link goes through the plain-name check on both ends.
func TestWhyNamesTheLinksOfEventAndCallRuns(t *testing.T) {
	e, l := whyEnv(t)
	l.runs["local/demo-alerts|r2"] = linkedChainRun("demo-reader.reading → demo-bridge.in", "demo-bridge.price → demo-alerts.price")
	out := e.why(t, "3")
	if len(out) != 1 || !strings.Contains(out[0].Text, "2. 16:30 demo-bridge v2, by an event over demo-reader.reading → demo-bridge.in · 0.1 s · ok") ||
		!strings.Contains(out[0].Text, "3. 16:30 demo-alerts v1, by a call over demo-bridge.price → demo-alerts.price · 0.0 s · ok") {
		t.Fatalf("why: %+v", out)
	}
	l.runs["local/demo-alerts|r2"] = linkedChainRun("demo-reader.reading → Evil *port*", "no arrow at all")
	out = e.why(t, "3")
	if len(out) != 1 || !strings.Contains(out[0].Text, "by an event over demo-reader.reading → ?") || !strings.Contains(out[0].Text, "by a call over ?") ||
		strings.Contains(out[0].Text, "Evil") || strings.Contains(out[0].Text, "no arrow") {
		t.Fatalf("why with odd links: %+v", out)
	}
}

// The model's note tells the same causes, links through the same check.
func TestTheModelsNoteNamesTheLinks(t *testing.T) {
	r := linkedChainRun("demo-reader.reading → demo-bridge.in", "demo-bridge.price → demo-alerts.price")
	got := chainWords(r, time.UTC)
	for _, want := range []string{"demo-bridge v2 ran at 16:30 by an event over demo-reader.reading → demo-bridge.in",
		"demo-alerts v1 ran at 16:30 by a call over demo-bridge.price → demo-alerts.price"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q lacks %q", got, want)
		}
	}
	if odd := chainWords(linkedChainRun("a.x → B Y", ""), time.UTC); !strings.Contains(odd, "by an event over a.x → ?") || !strings.Contains(odd, "by a call over ?") {
		t.Errorf("odd links: %q", odd)
	}
}
