package natsbus

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go"
)

func TestEdgePermissionsBase(t *testing.T) {
	p, err := EdgePermissions("e1", EdgeNeeds{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"$JSC.R.>", "g.src.e1.>", "g.tel.e1"}; !slices.Equal(p.Publish, want) {
		t.Fatalf("publish = %v, want %v", p.Publish, want)
	}
	if want := []string{"$JS.e1.API.>"}; !slices.Equal(p.Subscribe, want) {
		t.Fatalf("subscribe = %v, want %v", p.Subscribe, want)
	}
}

func TestEdgePermissionsFromLinks(t *testing.T) {
	n := EdgeNeeds{
		Calls:  []PortRef{{"reader", "read"}, {"reader", "read"}},
		Serves: []PortRef{{"fan", "speed"}},
		Extra:  UserPermissions{Subscribe: []string{"desired.e1.>"}},
	}
	p, err := EdgePermissions("e1", n)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, s := range p.Publish {
		if s == "g.rpc.core.reader.read" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("publish = %v: want the one call subject once", p.Publish)
	}
	for _, want := range []string{"g.rpc.e1.fan.speed", "desired.e1.>"} {
		if !slices.Contains(p.Subscribe, want) {
			t.Fatalf("subscribe = %v lacks %s", p.Subscribe, want)
		}
	}
	q, _ := EdgePermissions("e1", EdgeNeeds{Calls: n.Calls[:1], Serves: n.Serves, Extra: n.Extra})
	if !slices.Equal(p.Publish, q.Publish) || !slices.Equal(p.Subscribe, q.Subscribe) {
		t.Fatal("the same links gave different permissions")
	}
}

// An edge is never given a wildcard it did not ask for: a port, a module or
// an edge name with one in it is an error.
func TestEdgePermissionsRefuseWildcards(t *testing.T) {
	for _, n := range []EdgeNeeds{
		{Calls: []PortRef{{"reader", ">"}}},
		{Calls: []PortRef{{"*", "read"}}},
		{Serves: []PortRef{{"a.b", "x"}}},
	} {
		if _, err := EdgePermissions("e1", n); !errors.Is(err, bus.ErrBadToken) {
			t.Fatalf("%v: err = %v", n, err)
		}
	}
	for _, name := range []string{"core", "e.1", ">", ""} {
		if _, err := EdgePermissions(name, EdgeNeeds{}); err == nil {
			t.Fatalf("edge name %q was accepted", name)
		}
	}
}

// #369: edge A cannot write into edge B's source deliveries; its own it can.
func TestEdgeCannotWriteAnotherEdgesDeliveries(t *testing.T) {
	hub, port := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	perms := func(e string) UserPermissions {
		p, err := EdgePermissions(e, EdgeNeeds{})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := openEdge(t, filepath.Join(t.TempDir(), "a"), "ea", "nats-leaf://127.0.0.1:"+itoa(port), edgeCreds(t, hub, "ea", perms("ea")))
	var own, other atomic.Int32
	if _, err := hub.nc.Subscribe("g.src.ea.>", func(*nats.Msg) { own.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.nc.Subscribe("g.src.eb.>", func(*nats.Msg) { other.Add(1) }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return a.EdgeHealth(context.Background()).Linked })
	time.Sleep(300 * time.Millisecond) // the interest reaches the edge
	for range 20 {
		_ = a.nc.Publish("g.src.eb.S.forged", []byte("forged"))
		_ = a.nc.Publish("g.src.ea.S.mine", []byte("mine"))
	}
	waitFor(t, func() bool { return own.Load() > 0 })
	time.Sleep(500 * time.Millisecond)
	if n := other.Load(); n != 0 {
		t.Fatalf("%d messages reached another edge's deliveries", n)
	}
}

// #369: join and revoke call SourceEdge from several goroutines; none
// drops another's source.
func TestSourceChangesDoNotDropEachOther(t *testing.T) {
	hub, _ := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	ctx := context.Background()
	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := hub.SourceEdge(ctx, fmt.Sprintf("e%d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := hub.EdgeSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("%d edges sourced, want %d", len(got), n)
	}
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := hub.UnsourceEdge(ctx, fmt.Sprintf("e%d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, _ = hub.EdgeSources(ctx); len(got) != 0 {
		t.Fatalf("%d edges still sourced", len(got))
	}
}
