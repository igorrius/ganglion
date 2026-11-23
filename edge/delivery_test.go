package edge

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modstore"
)

const (
	testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	runDigest  = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// fakeDeliveryBus records its calls in order.
type fakeDeliveryBus struct {
	calls   []string
	desired []byte
	edges   []string
	actual  []byte
	prune   []string
}

func (f *fakeDeliveryBus) PutModule(_ context.Context, module, digest string, _, _ []byte) error {
	f.calls = append(f.calls, "module "+module+" "+digest)
	return nil
}

func (f *fakeDeliveryBus) PutDesired(_ context.Context, edge, module string, value []byte) error {
	f.calls = append(f.calls, "put "+edge+" "+module)
	f.desired = value
	return nil
}

func (f *fakeDeliveryBus) DeleteDesired(_ context.Context, edge, module string) error {
	f.calls = append(f.calls, "delete "+edge+" "+module)
	return nil
}

func (f *fakeDeliveryBus) DesiredEdges(context.Context, string) ([]string, error) {
	return f.edges, nil
}

func (f *fakeDeliveryBus) PruneModule(_ context.Context, module string, keep ...string) error {
	f.calls = append(f.calls, "prune "+module)
	f.prune = keep
	return nil
}

func (f *fakeDeliveryBus) Actual(context.Context, string, string) ([]byte, bool, error) {
	return f.actual, f.actual != nil, nil
}

func testActive() modstore.Active {
	return modstore.Active{
		Module:   "local/demo-reader",
		SHA256:   testDigest,
		Wasm:     []byte("wasm"),
		Manifest: []byte("name: demo-reader\nversion: 7\n"),
	}
}

func TestDeliverWritesFilesBeforeDesired(t *testing.T) {
	b := &fakeDeliveryBus{}
	cfg := map[string]any{"site": "x"}
	if err := (Deliverer{Bus: b}).Deliver(context.Background(), "home", testActive(), cfg); err != nil {
		t.Fatal(err)
	}
	want := []string{"module demo-reader " + testDigest, "put home demo-reader", "prune demo-reader"}
	if !slices.Equal(b.calls, want) {
		t.Fatalf("calls %v, want %v", b.calls, want)
	}
	d, err := decodeDesired("demo-reader", b.desired)
	if err != nil {
		t.Fatal(err)
	}
	if d.V != 1 || d.Module != "demo-reader" || d.Version != "7" || d.SHA256 != testDigest || d.Config["site"] != "x" {
		t.Fatalf("desired %+v", d)
	}
	if !slices.Equal(b.prune, []string{testDigest}) {
		t.Fatalf("keep %v", b.prune)
	}
}

func TestDeliverKeepsWhatTheEdgeRuns(t *testing.T) {
	b := &fakeDeliveryBus{actual: mustJSON(t, Actual{V: 1, Module: "demo-reader", SHA256: runDigest, Result: modstore.EdgeRunning})}
	if err := (Deliverer{Bus: b}).Deliver(context.Background(), "home", testActive(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.prune, []string{testDigest, runDigest}) {
		t.Fatalf("keep %v", b.prune)
	}
}

func TestDeliverRefusesABadManifestBeforeWriting(t *testing.T) {
	b := &fakeDeliveryBus{}
	a := testActive()
	a.Manifest = []byte("not: [a manifest")
	if err := (Deliverer{Bus: b}).Deliver(context.Background(), "home", a, nil); err == nil || len(b.calls) != 0 {
		t.Fatalf("err %v, calls %v", err, b.calls)
	}
}

func TestWithdraw(t *testing.T) {
	b := &fakeDeliveryBus{edges: []string{"home", "garage", "barn"}}
	if err := (Deliverer{Bus: b}).Withdraw(context.Background(), "demo-reader", "garage"); err != nil {
		t.Fatal(err)
	}
	want := []string{"delete home demo-reader", "delete barn demo-reader"}
	if !slices.Equal(b.calls, want) {
		t.Fatalf("calls %v, want %v", b.calls, want)
	}

	b = &fakeDeliveryBus{edges: []string{"home", "garage"}}
	if err := (Deliverer{Bus: b}).Withdraw(context.Background(), "demo-reader", ""); err != nil {
		t.Fatal(err)
	}
	want = []string{"delete home demo-reader", "delete garage demo-reader", "prune demo-reader"}
	if !slices.Equal(b.calls, want) || len(b.prune) != 0 {
		t.Fatalf("calls %v keep %v, want %v", b.calls, b.prune, want)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeDesired(t *testing.T) {
	good := func(mut func(*Desired)) []byte {
		d := Desired{V: 1, Module: "demo-reader", Version: "3", SHA256: testDigest}
		if mut != nil {
			mut(&d)
		}
		return mustJSON(t, d)
	}
	if d, err := decodeDesired("demo-reader", good(nil)); err != nil || d.Version != "3" || d.SHA256 != testDigest {
		t.Fatalf("good: %+v, %v", d, err)
	}
	big := good(func(d *Desired) { d.Config = map[string]any{"x": strings.Repeat("a", MaxDesired)} })
	for name, c := range map[string]struct {
		module string
		data   []byte
	}{
		"other module":  {"demo-reader", good(func(d *Desired) { d.Module = "other" })},
		"wrong format":  {"demo-reader", good(func(d *Desired) { d.V = 2 })},
		"short digest":  {"demo-reader", good(func(d *Desired) { d.SHA256 = "abc" })},
		"upper digest":  {"demo-reader", good(func(d *Desired) { d.SHA256 = strings.ToUpper(testDigest) })},
		"word version":  {"demo-reader", good(func(d *Desired) { d.Version = "1.2" })},
		"too large":     {"demo-reader", big},
		"bad name":      {"a.b", mustJSON(t, Desired{V: 1, Module: "a.b", Version: "1", SHA256: testDigest})},
		"not json":      {"demo-reader", []byte("{")},
		"bad name case": {"Demo Reader", mustJSON(t, Desired{V: 1, Module: "Demo Reader", Version: "1", SHA256: testDigest})},
	} {
		if _, err := decodeDesired(c.module, c.data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestActualIsReadWithCare(t *testing.T) {
	read := func(a Actual) (modstore.EdgeRun, bool) {
		t.Helper()
		b := &fakeDeliveryBus{actual: mustJSON(t, a)}
		run, ok, err := (Deliverer{Bus: b}).Actual(context.Background(), "home", "demo-reader")
		if err != nil {
			t.Fatal(err)
		}
		return run, ok
	}
	base := Actual{V: 1, Module: "demo-reader", Version: "4", SHA256: testDigest, Desired: runDigest, Result: modstore.EdgeRefused, Reason: "x"}

	run, ok := read(base)
	if !ok || run.Node != "home" || run.Version != "4" || run.SHA256 != testDigest || run.Desired != runDigest || run.Result != modstore.EdgeRefused {
		t.Fatalf("good: %+v, %v", run, ok)
	}
	other := base
	other.Module = "other"
	if _, ok := read(other); ok {
		t.Error("another module's value taken")
	}
	unknown := base
	unknown.Result = "exploded"
	if _, ok := read(unknown); ok {
		t.Error("an unknown result taken")
	}
	if _, ok, _ := (Deliverer{Bus: &fakeDeliveryBus{}}).Actual(context.Background(), "home", "demo-reader"); ok {
		t.Error("no value taken as an answer")
	}

	dirty := base
	dirty.Version, dirty.SHA256, dirty.Desired = "evil *x*", "nope", "NOPE"
	run, ok = read(dirty)
	if !ok || run.Version != "" || run.SHA256 != "" || run.Desired != "" {
		t.Fatalf("dirty: %+v, %v", run, ok)
	}

	long := base
	long.Reason = strings.Repeat("r", 1000)
	run, _ = read(long)
	if n := len([]rune(run.Reason)); n > maxReason+1 || n < maxReason {
		t.Fatalf("reason has %d runes", n)
	}
}
