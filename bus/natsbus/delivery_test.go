package natsbus

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func joinPerms(ps ...UserPermissions) UserPermissions {
	var out UserPermissions
	for _, p := range ps {
		out.Publish = append(out.Publish, p.Publish...)
		out.Subscribe = append(out.Subscribe, p.Subscribe...)
	}
	return out
}

// linkedEdge is a core and an edge e1 joined to it with the permissions
// of its events and of delivery.
func linkedEdge(t *testing.T) (hub, edge *Bus) {
	t.Helper()
	hub, port := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	creds := edgeCreds(t, hub, "e1", joinPerms(edgePerms("e1"), DeliveryPermissions("e1")))
	edge = openEdge(t, filepath.Join(t.TempDir(), "edge"), "e1", "nats-leaf://127.0.0.1:"+itoa(port), creds)
	waitFor(t, func() bool { return edge.EdgeHealth(context.Background()).Linked })
	return hub, edge
}

// watched collects what a watch hands over.
type watched struct {
	mu  sync.Mutex
	got []DesiredUpdate
}

func (w *watched) add(u DesiredUpdate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, u)
}

func (w *watched) all() []DesiredUpdate {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.got)
}

// The core writes a build and what e1 should run; e1, through its link and
// with only its own permissions, watches the key, reads the files and
// writes back what it runs.
func TestEdgeTakesItsDelivery(t *testing.T) {
	ctx := context.Background()
	hub, edge := linkedEdge(t)
	wasm, manifest := []byte("\x00asm-probe"), []byte("name: probe\n")
	if err := hub.PutModule(ctx, "probe", "d1", wasm, manifest); err != nil {
		t.Fatal(err)
	}
	if err := hub.PutDesired(ctx, "e1", "probe", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	var w watched
	stop, err := edge.WatchDesired(ctx, "e1", w.add)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop() }()
	waitFor(t, func() bool { return len(w.all()) == 1 })
	if u := w.all()[0]; u.Edge != "e1" || u.Module != "probe" || string(u.Value) != `{"v":1}` || u.Deleted {
		t.Fatalf("update = %+v", u)
	}
	gw, gm, err := edge.GetModule(ctx, "probe", "d1", 1<<20)
	if err != nil || !bytes.Equal(gw, wasm) || !bytes.Equal(gm, manifest) {
		t.Fatalf("GetModule = %q %q %v", gw, gm, err)
	}
	if _, _, err := edge.GetModule(ctx, "probe", "d1", 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a file over the limit was read: %v", err)
	}
	if err := edge.PutActual(ctx, "e1", "probe", []byte(`{"ok":1}`)); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := hub.Actual(ctx, "e1", "probe"); err != nil || !ok || string(v) != `{"ok":1}` {
		t.Fatalf("Actual = %q %v %v", v, ok, err)
	}

	// A change and a removal reach the watch; writing the same value again
	// is no change.
	if err := hub.PutDesired(ctx, "e1", "probe", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := hub.PutDesired(ctx, "e1", "probe", []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := hub.DeleteDesired(ctx, "e1", "probe"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(w.all()) == 3 })
	if got := w.all(); string(got[1].Value) != `{"v":2}` || !got[2].Deleted {
		t.Fatalf("updates = %+v", got)
	}
	if edges, err := hub.DesiredEdges(ctx, "probe"); err != nil || len(edges) != 0 {
		t.Fatalf("DesiredEdges after the delete = %v %v", edges, err)
	}
}

// What e1's user may not do of delivery is refused by the core's server:
// watching another edge's keys, writing a desired key or another edge's
// actual key, writing a module's file.
func TestEdgeDeliveryAbuse(t *testing.T) {
	ctx := context.Background()
	hub, edge := linkedEdge(t)
	if err := hub.PutDesired(ctx, "e2", "probe", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var w watched
	if stop, err := edge.WatchDesired(short, "e2", w.add); err == nil {
		time.Sleep(time.Second)
		_ = stop()
		if len(w.all()) != 0 {
			t.Fatalf("e1 read e2's desired keys: %+v", w.all())
		}
	}
	// The writes themselves, not a read before them: the bucket's and the
	// store's info are allowed, the puts are not.
	kv, err := edge.deliveryKV(short)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(short, "desired.e1.probe", []byte(`{"v":9}`)); err == nil {
		t.Fatal("e1 wrote a desired key")
	}
	if err := edge.PutActual(short, "e2", "probe", []byte(`{}`)); err == nil {
		t.Fatal("e1 wrote e2's actual key")
	}
	obs, err := edge.modulesStore(short)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := obs.PutBytes(short, "probe.d9.wasm", []byte("x")); err == nil {
		t.Fatal("e1 wrote a module's file")
	}
	if _, ok, _ := hub.Actual(ctx, "e2", "probe"); ok {
		t.Fatal("e2's actual key holds what e1 wrote")
	}
	if _, _, err := hub.GetModule(ctx, "probe", "d9", 10); err == nil {
		t.Fatal("the core holds the file e1 wrote")
	}
	if edges, err := hub.DesiredEdges(ctx, "probe"); err != nil || !slices.Equal(edges, []string{"e2"}) {
		t.Fatalf("DesiredEdges = %v %v", edges, err)
	}
}

// Pruning keeps the builds named and drops the module's others, and no
// other module's.
func TestPruneModule(t *testing.T) {
	ctx := context.Background()
	hub, _ := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	for _, d := range []string{"d1", "d2"} {
		if err := hub.PutModule(ctx, "probe", d, []byte("w"), []byte("m")); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub.PutModule(ctx, "other", "d1", []byte("w"), []byte("m")); err != nil {
		t.Fatal(err)
	}
	if err := hub.PruneModule(ctx, "probe", "d2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hub.GetModule(ctx, "probe", "d1", 10); err == nil {
		t.Fatal("a pruned build is still there")
	}
	for _, m := range [][2]string{{"probe", "d2"}, {"other", "d1"}} {
		if _, _, err := hub.GetModule(ctx, m[0], m[1], 10); err != nil {
			t.Fatalf("%s %s: %v", m[0], m[1], err)
		}
	}
}
