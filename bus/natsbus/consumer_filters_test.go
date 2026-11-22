package natsbus

import (
	"context"
	"testing"

	"github.com/igorrius/ganglion/bus"
)

// A consumer with several filters reads each of them and nothing else; its
// filters are brought to a new spec while the consumer keeps its name.
func TestAConsumerReadsSeveralFiltersAndTakesNewOnes(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	spec := linkSpec("cells_m", "")
	spec.Filters = []string{"g.ev.core.a.cells", "g.ev.core.b.cells"}
	got := make(chan bus.Delivery, 8)
	h := func(_ context.Context, d bus.Delivery) error { got <- d; return nil }
	stop := runConsumer(t, b, spec, h)
	publishEvent(t, b, "g.ev.core.c.cells", "c", "0") // not a filter
	publishEvent(t, b, "g.ev.core.a.cells", "a1", "1")
	publishEvent(t, b, "g.ev.core.b.cells", "b1", "2")
	for _, want := range []string{"a1", "b1"} {
		if d := receiveDelivery(t, got); d.ID != want {
			t.Fatalf("delivery %q, want %q", d.ID, want)
		}
	}
	stop()
	spec.Filters = []string{"g.ev.core.c.cells"}
	stop = runConsumer(t, b, spec, h)
	defer stop()
	publishEvent(t, b, "g.ev.core.a.cells", "a2", "3") // no longer a filter
	publishEvent(t, b, "g.ev.core.c.cells", "c2", "4")
	if d := receiveDelivery(t, got); d.ID != "c2" {
		t.Fatalf("delivery %q, want c2", d.ID)
	}
}
