package modstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
)

func eventHop(module, trigger, link string, at time.Time) modstore.Hop {
	h := hop(module, at)
	h.Trigger, h.Link = trigger, link
	return h
}

// An event run written before its producer's row has no chain; once the
// producer's row is there, Run and Runs show the chain, the producer's own
// filled the same way, and at most 4 hops (R-RUN-20).
func TestAnEventRunWrittenBeforeItsProducerGetsItsChainOnRead(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	write := func(r modstore.Run) {
		t.Helper()
		if err := s.RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// c ran by an event of b's run, which ran by a call of a's run; the
	// receivers' rows were written first, a's row last.
	write(modstore.Run{Hop: eventHop("local/c", "event", "b.x → c.y", t0.Add(2*time.Second)), ID: "rc", Producer: "local/b", ProducerRun: "rb", Depth: 2})
	write(modstore.Run{Hop: eventHop("local/b", "call", "a.q → b.s", t0.Add(time.Second)), ID: "rb", Producer: "local/a", ProducerRun: "ra", Depth: 1})
	if r, ok, err := s.Run(ctx, "local/c", "rc"); err != nil || !ok || len(r.Chain) != 1 {
		t.Fatalf("while a's row is missing: %+v, %v, %v", r.Chain, ok, err)
	}
	write(modstore.Run{Hop: hop("local/a", t0), ID: "ra"})
	r, ok, err := s.Run(ctx, "local/c", "rc")
	if err != nil || !ok || len(r.Chain) != 2 || r.Chain[0].Module != "local/a" || r.Chain[1].Module != "local/b" || r.Chain[1].Link != "a.q → b.s" {
		t.Fatalf("chain %+v, %v, %v", r.Chain, ok, err)
	}
	runs, err := s.Runs(ctx, "local/c", 5)
	if err != nil || len(runs) != 1 || len(runs[0].Chain) != 2 {
		t.Fatalf("runs %+v, %v", runs, err)
	}
	if raw, _, _ := s.Run(ctx, "local/a", "ra"); len(raw.Chain) != 0 {
		t.Fatalf("a cron run got a chain: %+v", raw.Chain)
	}
}

// A chain read from the producers never holds more than 4 hops.
func TestAFilledChainHoldsAtMostFourHops(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	for i := 7; i >= 0; i-- { // the receivers' rows first
		id := string(rune('a' + i))
		r := modstore.Run{Hop: eventHop("local/m"+id, "event", "x.y → z.w", t0.Add(time.Duration(i)*time.Second)), ID: id, Depth: i}
		if i > 0 {
			r.Producer, r.ProducerRun = "local/m"+string(rune('a'+i-1)), string(rune('a'+i-1))
		}
		if i == 0 {
			r.Trigger = "cron"
		}
		if err := s.RecordRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	r, ok, err := s.Run(ctx, "local/mh", "h")
	if err != nil || !ok || len(r.Chain) != 4 || r.Chain[3].Module != "local/mg" {
		t.Fatalf("chain %+v, %v, %v", r.Chain, ok, err)
	}
}
