package natsbus

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go"
)

// M10 abuse case (#324): an edge that writes the core's cell, or an event
// or a call that is not its own, is refused by the core's server and
// changes nothing. The control writes are the edge's own and must work, so
// a refusal is never the effect of a broken link.
func TestEdgeCannotWriteTheCoresCellsOrSpeakForTheCore(t *testing.T) {
	ctx := context.Background()
	hub, edge := linkedEdge(t)

	kv, err := hub.js.KeyValue(ctx, bus.BucketCells)
	if err != nil {
		t.Fatal(err)
	}
	const key = "reader.temp"
	rev, err := kv.Create(ctx, key, []byte(`{"desired":"mine"}`))
	if err != nil {
		t.Fatal(err)
	}

	var forgedEvents, forgedCalls atomic.Int32
	for subj, n := range map[string]*atomic.Int32{
		"g.ev.core.>": &forgedEvents, // the core's modules' events
		"g.ev.e2.>":   &forgedEvents, // another edge's events
		"g.rpc.>":     &forgedCalls,  // a call nobody linked
	} {
		if _, err := hub.nc.Subscribe(subj, func(*nats.Msg) { n.Add(1) }); err != nil {
			t.Fatal(err)
		}
	}
	// Control: the edge's own key is writable, so the link and the
	// permissions work and the refusals below are the server's.
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := edge.PutActual(short, "e1", "probe", []byte(`{}`)); err != nil {
		t.Fatalf("the edge cannot write its own key, so the refusals below prove nothing: %v", err)
	}

	// The cell, through the core's JetStream domain: the one way across
	// the link.
	_, err = edge.nc.RequestWithContext(short, "$JS."+bus.CoreNode+".API.$KV."+bus.BucketCells+"."+key, []byte(`{"desired":"forged"}`))
	refusedByServer(t, "writing the core's cell through its domain", err)
	// The plain KV subject stays on the edge: its own JetStream answers
	// from its own domain, and the core's cell is checked below.
	m, err := edge.nc.RequestWithContext(short, "$KV."+bus.BucketCells+"."+key, []byte(`{"desired":"local"}`))
	if err == nil && !strings.Contains(string(m.Data), `"domain":"e1"`) {
		t.Fatalf("the plain KV subject was answered outside the edge's domain: %s", m.Data)
	}
	refusedByServer(t, "reading the core's cells bucket", func() error {
		js, err := edge.hub()
		if err != nil {
			return err
		}
		_, err = js.KeyValue(short, bus.BucketCells)
		return err
	}())

	for _, subj := range []string{"g.ev.core.reader.reading", "g.ev.e2.x.y", "g.rpc.core.reader.read"} {
		if err := edge.nc.Publish(subj, []byte(`{"forged":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := edge.nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // a forged message would be here by now

	e, err := kv.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if e.Revision() != rev || string(e.Value()) != `{"desired":"mine"}` {
		t.Fatalf("the cell changed: revision %d (was %d), value %s", e.Revision(), rev, e.Value())
	}
	if n := forgedEvents.Load(); n != 0 {
		t.Fatalf("%d forged events reached the core's subjects", n)
	}
	if n := forgedCalls.Load(); n != 0 {
		t.Fatalf("%d calls reached the core without a link", n)
	}
}
