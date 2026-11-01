package cells_test

import (
	"context"
	"encoding/json"
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
	store := newStore(t)
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

// failing always fails to apply.
type failing struct{}

func (failing) Apply(context.Context, cells.Cell, cells.Reporter) error {
	return errors.New("plug offline")
}

func TestExternalReportBeforeCommandIsNotManual(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	v := virtual.New()
	s := cells.NewService(store, cells.WithBackend("virtual", v))
	if err := s.Define(ctx, cells.Def{ID: "load.a", Kind: cells.KindSwitch, Backend: "virtual"}); err != nil {
		t.Fatal(err)
	}
	if err := v.Flip(ctx, s, "load.a", off); err != nil { // someone switched it off by hand, earlier
		t.Fatal(err)
	}
	broken := cells.NewService(store, cells.WithBackend("virtual", failing{}))
	c, _ := broken.SetDesired(ctx, "load.a", on, "flow", 0) // the flow commands on; apply fails
	if c.Manual() {
		t.Fatalf("a command after an external report is not manual: %+v", c)
	}
	n, err := s.Reconcile(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Reconcile = %d (%v), want 1", n, err)
	}
	if got, _ := s.Get(ctx, "load.a"); !got.InSync() || string(got.Reported) != `"on"` {
		t.Fatalf("after reconcile: %+v", got)
	}
}

func TestManualOnlyForCommandedSwitches(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "battery.soc", cells.KindSensor)
	define(t, s, "load.a", cells.KindSwitch)
	sensor, _ := s.Report(ctx, "battery.soc", json.RawMessage(`57`), cells.CauseExternal, "module")
	sw, _ := s.Report(ctx, "load.a", on, cells.CauseExternal, "startup poll")
	if sensor.Manual() || sw.Manual() {
		t.Fatalf("manual: sensor=%v switch without desired=%v; want both false", sensor.Manual(), sw.Manual())
	}
}

func TestSetDesiredReturnsTheCellAfterASyncReport(t *testing.T) {
	ctx := context.Background()
	s := cells.NewService(newStore(t), cells.WithBackend("virtual", virtual.New()))
	if err := s.Define(ctx, cells.Def{ID: "load.a", Kind: cells.KindSwitch, Backend: "virtual"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.SetDesired(ctx, "load.a", on, "flow", 0)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Get(ctx, "load.a")
	if c.Version != stored.Version || !c.InSync() {
		t.Fatalf("returned version %d in sync=%v; stored version %d", c.Version, c.InSync(), stored.Version)
	}
	if _, err := s.SetDesired(ctx, "load.a", off, "flow", c.Version); err != nil {
		t.Fatalf("chaining expectVersion from the result: %v", err)
	}
}

// failsFor fails to apply one cell and applies the rest.
type failsFor struct {
	bad string
	ok  cells.Backend
}

func (f failsFor) Apply(ctx context.Context, c cells.Cell, r cells.Reporter) error {
	if c.ID == f.bad {
		return errors.New("plug offline")
	}
	return f.ok.Apply(ctx, c, r)
}

func TestReconcileContinuesPastFailures(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	before := cells.NewService(store, cells.WithBackend("b", failing{}))
	for _, id := range []string{"load.a", "load.b"} {
		if err := before.Define(ctx, cells.Def{ID: id, Kind: cells.KindSwitch, Backend: "b"}); err != nil {
			t.Fatal(err)
		}
		_, _ = before.SetDesired(ctx, id, on, "flow", 0)
	}
	after := cells.NewService(store, cells.WithBackend("b", failsFor{bad: "load.a", ok: virtual.New()}))
	n, err := after.Reconcile(ctx)
	if err == nil || n != 1 {
		t.Fatalf("Reconcile = %d (%v), want 1 applied and an error for load.a", n, err)
	}
	if b, _ := after.Get(ctx, "load.b"); !b.InSync() {
		t.Fatalf("load.b not reconciled behind a failing load.a: %+v", b)
	}
}
