package cells_test

import (
	"context"
	"errors"
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/virtual"
)

func TestDefineRejectsUnknownBackend(t *testing.T) {
	s, _ := newService(t)
	if err := s.Define(context.Background(), cells.Def{ID: "load.a", Kind: cells.KindSwitch, Backend: "tuya"}); !errors.Is(err, cells.ErrInvalidValue) {
		t.Fatalf("Define with an unknown backend = %v, want ErrInvalidValue", err)
	}
}

func TestLateCommandedReportIsNotManual(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)
	_, _ = s.SetDesired(ctx, "load.a", on, "flow", 0)
	_, _ = s.SetDesired(ctx, "load.a", off, "flow", 0)
	// The backend now reports the result of the earlier "on" command.
	c, err := s.Report(ctx, "load.a", on, cells.CauseCommanded, "backend:virtual")
	if err != nil {
		t.Fatal(err)
	}
	if c.InSync() || c.Manual() {
		t.Fatalf("late commanded report: insync=%v manual=%v, want out of sync but not manual", c.InSync(), c.Manual())
	}
}

// crashing never applies: it stands for a process that died after storing
// desired and before the backend acted.
type crashing struct{}

func (crashing) Apply(context.Context, cells.Cell, cells.Reporter) error {
	return errors.New("process died")
}

func TestReconcileReappliesAfterCrash(t *testing.T) {
	ctx := context.Background()
	store := cells.NewMemStore()
	before := cells.NewService(store, cells.WithBackend("virtual", crashing{}))
	for _, id := range []string{"load.a", "load.b"} {
		if err := before.Define(ctx, cells.Def{ID: id, Kind: cells.KindSwitch, Backend: "virtual"}); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = before.SetDesired(ctx, "load.a", on, "flow", 0)

	v := virtual.New()
	after := cells.NewService(store, cells.WithBackend("virtual", v))
	_, _ = after.SetDesired(ctx, "load.b", off, "flow", 0)
	if err := v.Flip(ctx, after, "load.b", on); err != nil { // someone switched it on by hand
		t.Fatal(err)
	}

	n, err := after.Reconcile(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Reconcile = %d (%v), want 1", n, err)
	}
	if a, _ := after.Get(ctx, "load.a"); !a.InSync() || string(a.Reported) != `"on"` {
		t.Fatalf("load.a after reconcile: %+v", a)
	}
	if b, _ := after.Get(ctx, "load.b"); !b.Manual() {
		t.Fatalf("load.b should stay manual: %+v", b)
	}
}
