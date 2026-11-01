// Package cellstest is the conformance suite every cells.Backend must pass.
package cellstest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
)

// Flipper changes a device's state from outside, as a person would by hand.
type Flipper interface {
	Flip(ctx context.Context, r cells.Reporter, id string, v json.RawMessage) error
}

// Eventually polls cond until it holds or a few seconds pass.
func Eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Run runs the suite. newBackend returns a fresh backend and a way to flip
// its devices externally.
func Run(t *testing.T, newBackend func(t *testing.T) (cells.Backend, Flipper)) {
	setup := func(t *testing.T) (*cells.Service, Flipper) {
		b, f := newBackend(t)
		s := cells.NewService(cells.NewMemStore(), cells.WithBackend("dut", b))
		if err := s.Define(t.Context(), cells.Def{ID: "load.a", Kind: cells.KindSwitch, Backend: "dut"}); err != nil {
			t.Fatal(err)
		}
		return s, f
	}
	get := func(t *testing.T, s *cells.Service) cells.Cell {
		c, err := s.Get(t.Context(), "load.a")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("desired converges with a commanded report", func(t *testing.T) {
		s, _ := setup(t)
		if _, err := s.SetDesired(t.Context(), "load.a", json.RawMessage(`"on"`), "test", 0); err != nil {
			t.Fatal(err)
		}
		Eventually(t, "in sync on", func() bool { return get(t, s).InSync() })
		if c := get(t, s); c.ReportedCause != cells.CauseCommanded || c.Manual() {
			t.Fatalf("cell = %+v", c)
		}
	})

	t.Run("a second change converges too", func(t *testing.T) {
		s, _ := setup(t)
		_, _ = s.SetDesired(t.Context(), "load.a", json.RawMessage(`"on"`), "test", 0)
		Eventually(t, "in sync on", func() bool { return get(t, s).InSync() })
		_, _ = s.SetDesired(t.Context(), "load.a", json.RawMessage(`"off"`), "test", 0)
		Eventually(t, "in sync off", func() bool { c := get(t, s); return c.InSync() && string(c.Reported) == `"off"` })
	})

	t.Run("quick successive changes converge to the last desired", func(t *testing.T) {
		for i := range 50 {
			s, _ := setup(t)
			_, _ = s.SetDesired(t.Context(), "load.a", json.RawMessage(`"on"`), "test", 0)
			_, _ = s.SetDesired(t.Context(), "load.a", json.RawMessage(`"off"`), "test", 0)
			Eventually(t, "in sync off", func() bool { c := get(t, s); return c.InSync() && string(c.Reported) == `"off"` })
			time.Sleep(30 * time.Millisecond) // a late report must not undo it
			if c := get(t, s); !c.InSync() || string(c.Reported) != `"off"` {
				t.Fatalf("run %d: ended with %+v", i, c)
			}
		}
	})

	t.Run("an external flip is manual", func(t *testing.T) {
		s, f := setup(t)
		_, _ = s.SetDesired(t.Context(), "load.a", json.RawMessage(`"off"`), "test", 0)
		Eventually(t, "in sync off", func() bool { return get(t, s).InSync() })
		if err := f.Flip(t.Context(), s, "load.a", json.RawMessage(`"on"`)); err != nil {
			t.Fatal(err)
		}
		Eventually(t, "manual", func() bool { return get(t, s).Manual() })
	})
}
