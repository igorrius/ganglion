package virtual_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/cellstest"
	"github.com/igorrius/ganglion/cells/virtual"
)

func TestConformance(t *testing.T) {
	cellstest.Run(t, func(*testing.T) (cells.Backend, cellstest.Flipper) {
		b := virtual.New()
		return b, b
	})
}

func TestConformanceWithDelay(t *testing.T) {
	cellstest.Run(t, func(*testing.T) (cells.Backend, cellstest.Flipper) {
		b := virtual.New(virtual.WithDelay(20 * time.Millisecond))
		return b, b
	})
}

func setup(t *testing.T, b *virtual.Backend) *cells.Service {
	s := cells.NewService(cells.NewMemStore(), cells.WithBackend("virtual", b))
	if err := s.Define(t.Context(), cells.Def{ID: "load.a", Kind: cells.KindSwitch, Backend: "virtual"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDelayKeepsCellOutOfSyncUntilItPasses(t *testing.T) {
	s := setup(t, virtual.New(virtual.WithDelay(150*time.Millisecond)))
	c, err := s.SetDesired(t.Context(), "load.a", json.RawMessage(`"on"`), "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.InSync() {
		t.Fatal("in sync before the delay passed")
	}
	cellstest.Eventually(t, "in sync after the delay", func() bool { c, _ := s.Get(t.Context(), "load.a"); return c.InSync() })
}

func TestFailNextRecordsAnErrorAndRecovers(t *testing.T) {
	b := virtual.New()
	s := setup(t, b)
	boom := errors.New("plug does not answer")
	b.FailNext(1, boom)

	c, err := s.SetDesired(t.Context(), "load.a", json.RawMessage(`"on"`), "test", 0)
	if !errors.Is(err, boom) {
		t.Fatalf("SetDesired error = %v, want the injected failure", err)
	}
	if c.InSync() {
		t.Fatal("in sync after a failed apply")
	}
	h, _ := s.History(t.Context(), "load.a", 1)
	if len(h) != 1 || h[0].Field != cells.FieldError {
		t.Fatalf("last change = %+v, want an error change", h)
	}
	if _, err := s.SetDesired(t.Context(), "load.a", json.RawMessage(`"on"`), "test", 0); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if c, _ := s.Get(t.Context(), "load.a"); !c.InSync() {
		t.Fatalf("not in sync after the retry: %+v", c)
	}
}
