package cells_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newStore makes the store the service tests run on; TestServiceOnSQLite
// swaps it for SQLite.
var newStore = func(*testing.T) cells.Store { return cells.NewMemStore() }

func newService(t *testing.T, opts ...cells.Option) (*cells.Service, *fakeClock) {
	t.Helper()
	clk := newClock()
	return cells.NewService(newStore(t), append([]cells.Option{cells.WithClock(clk.Now)}, opts...)...), clk
}

var (
	on  = json.RawMessage(`"on"`)
	off = json.RawMessage(`"off"`)
)

func define(t *testing.T, s *cells.Service, id string, kind cells.Kind) {
	t.Helper()
	if err := s.Define(context.Background(), cells.Def{ID: id, Kind: kind}); err != nil {
		t.Fatal(err)
	}
}

func TestDefineIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "load.big_boiler", cells.KindSwitch)
	define(t, s, "load.big_boiler", cells.KindSwitch)
	c, err := s.Get(ctx, "load.big_boiler")
	if err != nil || c.Version != 1 || c.Kind != cells.KindSwitch {
		t.Fatalf("cell = %+v (%v)", c, err)
	}
	if err := s.Define(ctx, cells.Def{ID: "load.big_boiler", Kind: cells.KindSensor}); !errors.Is(err, cells.ErrConflict) {
		t.Fatalf("redefining with another kind = %v, want ErrConflict", err)
	}
}

func TestDefineRejectsBadID(t *testing.T) {
	s, _ := newService(t)
	for _, id := range []string{"", "Load", "load..a", ".a", "load.a b", "9load", "-load"} {
		if err := s.Define(context.Background(), cells.Def{ID: id, Kind: cells.KindSwitch}); !errors.Is(err, cells.ErrInvalidID) {
			t.Errorf("Define(%q) = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestSwitchAcceptsOnlyOnOff(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)
	for _, v := range []json.RawMessage{on, off} {
		if _, err := s.SetDesired(ctx, "load.a", v, "test", 0); err != nil {
			t.Errorf("SetDesired(%s) = %v", v, err)
		}
	}
	for _, v := range []string{`true`, `"ON"`, `1`, `null`, `not json`} {
		if _, err := s.SetDesired(ctx, "load.a", json.RawMessage(v), "test", 0); !errors.Is(err, cells.ErrInvalidValue) {
			t.Errorf("SetDesired(%s) = %v, want ErrInvalidValue", v, err)
		}
	}
}

func TestSensorRejectsDesired(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "battery.soc", cells.KindSensor)
	if _, err := s.SetDesired(ctx, "battery.soc", json.RawMessage(`57`), "test", 0); !errors.Is(err, cells.ErrWrongKind) {
		t.Fatalf("SetDesired on a sensor = %v, want ErrWrongKind", err)
	}
	c, err := s.Report(ctx, "battery.soc", json.RawMessage(`57`), cells.CauseExternal, "module:deye-reader")
	if err != nil || string(c.Reported) != `57` {
		t.Fatalf("Report on a sensor = %+v (%v)", c, err)
	}
}

func TestSetDesiredWithStaleVersionConflicts(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)
	c, err := s.SetDesired(ctx, "load.a", on, "flow", 1)
	if err != nil || c.Version != 2 {
		t.Fatalf("first SetDesired = %+v (%v)", c, err)
	}
	if _, err := s.SetDesired(ctx, "load.a", off, "agent", 1); !errors.Is(err, cells.ErrConflict) {
		t.Fatalf("stale SetDesired = %v, want ErrConflict", err)
	}
	if got, _ := s.Get(ctx, "load.a"); string(got.Desired) != `"on"` {
		t.Fatalf("stale write changed desired to %s", got.Desired)
	}
}

func TestReportedCauseAndManual(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)
	if _, err := s.SetDesired(ctx, "load.a", off, "flow", 0); err != nil {
		t.Fatal(err)
	}
	c, err := s.Report(ctx, "load.a", off, cells.CauseCommanded, "backend:virtual")
	if err != nil || !c.InSync() || c.Manual() {
		t.Fatalf("after commanded report: %+v insync=%v manual=%v (%v)", c, c.InSync(), c.Manual(), err)
	}
	c, err = s.Report(ctx, "load.a", on, cells.CauseExternal, "backend:virtual")
	if err != nil || c.InSync() || !c.Manual() {
		t.Fatalf("after external report: insync=%v manual=%v (%v)", c.InSync(), c.Manual(), err)
	}
}

func TestInSyncIgnoresJSONFormatting(t *testing.T) {
	c := cells.Cell{Desired: json.RawMessage(`"on"`), Reported: json.RawMessage(` "on" `)}
	if !c.InSync() {
		t.Fatal("equal values with different whitespace are not in sync")
	}
}

func collect(t *testing.T, ch <-chan cells.Change, n int) []cells.Change {
	t.Helper()
	var got []cells.Change
	timeout := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatalf("watch closed after %d of %d changes", len(got), n)
			}
			got = append(got, c)
		case <-timeout:
			t.Fatalf("got %d of %d changes", len(got), n)
		}
	}
	return got
}

func TestWatchReplaysThenStreams(t *testing.T) {
	ctx := t.Context()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)                      // change 1
	_, _ = s.SetDesired(ctx, "load.a", on, "flow", 0)             // change 2
	_, _ = s.Report(ctx, "load.a", on, cells.CauseCommanded, "b") // change 3

	w := s.Watch(ctx, 0)
	first := collect(t, w, 3)
	_, _ = s.SetDesired(ctx, "load.a", off, "flow", 0) // change 4, live
	live := collect(t, w, 1)

	all := append(first, live...)
	wantFields := []string{cells.FieldCreated, cells.FieldDesired, cells.FieldReported, cells.FieldDesired}
	for i, c := range all {
		if c.Field != wantFields[i] || (i > 0 && c.Seq <= all[i-1].Seq) {
			t.Fatalf("change %d = %+v; all = %+v", i, c, all)
		}
	}
}

func TestWatchFromSeqSkipsOlder(t *testing.T) {
	ctx := t.Context()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)
	c, _ := s.SetDesired(ctx, "load.a", on, "flow", 0)
	h, _ := s.History(ctx, "load.a", 1)
	_, _ = s.SetDesired(ctx, "load.a", off, "flow", c.Version)

	got := collect(t, s.Watch(ctx, h[0].Seq), 1)
	if string(got[0].Value) != `"off"` {
		t.Fatalf("first change after seq %d = %+v", h[0].Seq, got[0])
	}
}

func TestSlowWatcherLosesNothing(t *testing.T) {
	ctx := t.Context()
	s, _ := newService(t)
	define(t, s, "battery.soc", cells.KindSensor)
	w := s.Watch(ctx, 0)
	for i := range 200 {
		if _, err := s.Report(ctx, "battery.soc", json.RawMessage([]byte{'1', byte('0' + i%10)}), cells.CauseExternal, "m"); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // the reader is slow
	got := collect(t, w, 201)
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("out of order at %d", i)
		}
	}
}

func TestWatchClosesWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := newService(t)
	w := s.Watch(ctx, 0)
	cancel()
	select {
	case _, ok := <-w:
		if ok {
			t.Fatal("unexpected change")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not close")
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func TestDefineAcceptsDashedIDs(t *testing.T) {
	s, _ := newService(t)
	define(t, s, "deye-reader.soc", cells.KindSensor)
	if _, err := s.Get(context.Background(), "deye-reader.soc"); err != nil {
		t.Fatal(err)
	}
}

func defineOwned(t *testing.T, s *cells.Service, owner, about string) error {
	t.Helper()
	return s.DefineOwned(context.Background(), cells.OwnedDef{ID: "deye-reader.soc", Owner: owner, About: about, Kind: cells.KindSensor})
}

func TestDefineOwnedCreatesWithOwnerAndAbout(t *testing.T) {
	s, _ := newService(t)
	if err := defineOwned(t, s, "deye-reader", "battery charge, %"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(context.Background(), "deye-reader.soc")
	if err != nil || c.Owner != "deye-reader" || c.About != "battery charge, %" || c.Kind != cells.KindSensor {
		t.Fatalf("cell = %+v (%v)", c, err)
	}
}

func TestDefineOwnedByAnotherOwnerIsRefused(t *testing.T) {
	s, _ := newService(t)
	if err := defineOwned(t, s, "deye-reader", "a"); err != nil {
		t.Fatal(err)
	}
	if err := defineOwned(t, s, "thief", "b"); !errors.Is(err, cells.ErrOwned) {
		t.Fatalf("second owner = %v, want ErrOwned", err)
	}
	if c, _ := s.Get(context.Background(), "deye-reader.soc"); c.Owner != "deye-reader" || c.About != "a" {
		t.Fatalf("cell changed: %+v", c)
	}
}

func TestDefineOwnedSameOwnerUpdatesAbout(t *testing.T) {
	s, _ := newService(t)
	_ = defineOwned(t, s, "deye-reader", "old")
	if err := defineOwned(t, s, "deye-reader", "new"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(context.Background(), "deye-reader.soc")
	if c.About != "new" || c.Version != 1 {
		t.Fatalf("cell = %+v", c)
	}
}

func TestDefineOwnedOfAnotherKindConflicts(t *testing.T) {
	s, _ := newService(t)
	define(t, s, "deye-reader.soc", cells.KindSwitch)
	err := defineOwned(t, s, "deye-reader", "soc")
	if !errors.Is(err, cells.ErrConflict) || !strings.Contains(err.Error(), "deye-reader.soc") {
		t.Fatalf("a switch taken as a sensor = %v, want ErrConflict naming the cell", err)
	}
	if c, _ := s.Get(context.Background(), "deye-reader.soc"); c.Owner != "" || c.Kind != cells.KindSwitch {
		t.Fatalf("cell changed: %+v", c)
	}
}

func TestDefineOwnedTakesAnOwnerlessCell(t *testing.T) {
	s, _ := newService(t)
	define(t, s, "deye-reader.soc", cells.KindSensor)
	if err := defineOwned(t, s, "deye-reader", "soc"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Get(context.Background(), "deye-reader.soc"); c.Owner != "deye-reader" {
		t.Fatalf("cell = %+v", c)
	}
}

func TestDefineOwnedValidates(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	if err := s.DefineOwned(ctx, cells.OwnedDef{ID: "Bad", Owner: "m", Kind: cells.KindSensor}); !errors.Is(err, cells.ErrInvalidID) {
		t.Errorf("bad id = %v", err)
	}
	if err := s.DefineOwned(ctx, cells.OwnedDef{ID: "m.a", Owner: "", Kind: cells.KindSensor}); err == nil {
		t.Error("empty owner accepted")
	}
	if err := s.DefineOwned(ctx, cells.OwnedDef{ID: "m.a", Owner: "m", Kind: cells.KindRequest}); !errors.Is(err, cells.ErrWrongKind) {
		t.Errorf("request kind = %v", err)
	}
}

func TestDisownThenRedefineKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	_ = defineOwned(t, s, "deye-reader", "soc")
	if _, err := s.Report(ctx, "deye-reader.soc", json.RawMessage(`55`), cells.CauseExternal, "module:deye-reader"); err != nil {
		t.Fatal(err)
	}
	define(t, s, "other.x", cells.KindSensor)
	n, err := s.Disown(ctx, "deye-reader")
	if err != nil || n != 1 {
		t.Fatalf("Disown = %d, %v", n, err)
	}
	if c, _ := s.Get(ctx, "deye-reader.soc"); c.Owner != "" {
		t.Fatalf("owner after Disown = %q", c.Owner)
	}
	if err := defineOwned(t, s, "deye-reader", "soc again"); err != nil {
		t.Fatal(err)
	}
	h, _ := s.History(ctx, "deye-reader.soc", 10)
	if len(h) != 2 || string(h[0].Value) != "55" {
		t.Fatalf("history = %+v", h)
	}
	if n, err := s.Disown(ctx, "nobody"); err != nil || n != 0 {
		t.Fatalf("Disown(nobody) = %d, %v", n, err)
	}
}

func TestListReturnsOwnerAndAbout(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	define(t, s, "load.a", cells.KindSwitch)
	_ = defineOwned(t, s, "deye-reader", "soc")
	l, err := s.List(ctx)
	if err != nil || len(l) != 2 || l[0].ID != "deye-reader.soc" || l[0].Owner != "deye-reader" || l[0].About != "soc" || l[1].Owner != "" {
		t.Fatalf("list = %+v (%v)", l, err)
	}
}

func TestHistorySinceIsOldestFirstAndLimited(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	_ = defineOwned(t, s, "deye-reader", "soc")
	start := clk.Now()
	for i := range 4 {
		clk.Add(time.Minute)
		if _, err := s.Report(ctx, "deye-reader.soc", json.RawMessage(strconv.Itoa(i)), cells.CauseExternal, "m"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.HistorySince(ctx, "deye-reader.soc", start.Add(90*time.Second), 10)
	if err != nil || len(got) != 3 || string(got[0].Value) != "1" || string(got[2].Value) != "3" {
		t.Fatalf("since = %+v (%v)", got, err)
	}
	got, _ = s.HistorySince(ctx, "deye-reader.soc", start.Add(90*time.Second), 2)
	if len(got) != 2 || string(got[1].Value) != "2" {
		t.Fatalf("limited = %+v", got)
	}
	if got, _ = s.HistorySince(ctx, "deye-reader.soc", start.Add(time.Hour), 10); len(got) != 0 {
		t.Fatalf("future = %+v", got)
	}
}
