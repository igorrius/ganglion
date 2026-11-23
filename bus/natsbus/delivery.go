package natsbus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// This file is module delivery to edges (R-EDGE-6, ADR-0117). The core
// writes a build's files into the modules Object Store and what an edge
// should run into desired.<edge>.<module>; the edge reads both across its
// link from the core's JetStream domain and writes actual.<edge>.<module>.
// What a value means is the edge package's; here are the bytes.

// ErrTooLarge is a module's object over the size the reader takes.
var ErrTooLarge = errors.New("natsbus: the module's file is larger than an edge takes")

// DeliveryPermissions are what an edge's user needs of module delivery
// (R-EDGE-2, ADR-0117), measured on nats-server 2.15.3: read the delivery
// bucket's and the Object Store's info, watch its own desired keys and no
// other edge's, read the modules' objects, write its own actual keys, and
// take replies under its own inbox prefix (EdgeInbox) and no one else's.
// It writes no desired key and no object. The join adds them to the rest
// of the edge's permissions.
func DeliveryPermissions(edge string) UserPermissions {
	const api = "$JS." + bus.CoreNode + ".API."
	kv, obj := "KV_"+bus.BucketDelivery, "OBJ_"+bus.ObjectModules
	return UserPermissions{
		Publish: []string{
			api + "STREAM.INFO." + kv,
			api + "STREAM.INFO." + obj,
			api + "CONSUMER.CREATE." + kv + ".*.$KV." + bus.BucketDelivery + ".desired." + edge + ".*",
			api + "CONSUMER.DELETE." + kv + ".*",
			api + "DIRECT.GET." + obj + ".$O." + bus.ObjectModules + ".M.*",
			api + "CONSUMER.CREATE." + obj + ".*.$O." + bus.ObjectModules + ".C.*",
			api + "CONSUMER.DELETE." + obj + ".*",
			api + "$KV." + bus.BucketDelivery + ".actual." + edge + ".*",
		},
		Subscribe: []string{EdgeInbox(edge) + ".>"},
	}
}

// hub is the JetStream the delivery lives in: the node's own on the core,
// the core's domain on an edge.
func (b *Bus) hub() (jetstream.JetStream, error) {
	if b.nc == nil {
		return nil, bus.ErrNotConnected
	}
	if !b.IsEdge() {
		return b.js, nil
	}
	js, err := jetstream.NewWithDomain(b.nc, bus.CoreNode)
	if err != nil {
		return nil, fmt.Errorf("natsbus: the core's jetstream: %w", err)
	}
	return js, nil
}

func (b *Bus) deliveryKV(ctx context.Context) (jetstream.KeyValue, error) {
	js, err := b.hub()
	if err != nil {
		return nil, err
	}
	kv, err := js.KeyValue(ctx, bus.BucketDelivery)
	if err != nil {
		return nil, fmt.Errorf("natsbus: bucket %s: %w", bus.BucketDelivery, err)
	}
	return kv, nil
}

func (b *Bus) modulesStore(ctx context.Context) (jetstream.ObjectStore, error) {
	js, err := b.hub()
	if err != nil {
		return nil, err
	}
	obs, err := js.ObjectStore(ctx, bus.ObjectModules)
	if err != nil {
		return nil, fmt.Errorf("natsbus: object store %s: %w", bus.ObjectModules, err)
	}
	return obs, nil
}

// PutModule writes a build's two files under module and digest, once: an
// object already there is left as it is, since its name holds the digest
// its content is checked by.
func (b *Bus) PutModule(ctx context.Context, module, digest string, wasm, manifest []byte) error {
	obs, err := b.modulesStore(ctx)
	if err != nil {
		return err
	}
	for _, p := range []struct {
		part string
		data []byte
	}{{bus.PartWasm, wasm}, {bus.PartManifest, manifest}} {
		name, err := bus.ModuleObject(module, digest, p.part)
		if err != nil {
			return err
		}
		if _, err := obs.GetInfo(ctx, name); err == nil {
			continue
		} else if !errors.Is(err, jetstream.ErrObjectNotFound) {
			return fmt.Errorf("natsbus: object %s: %w", name, err)
		}
		if _, err := obs.PutBytes(ctx, name, p.data); err != nil {
			return fmt.Errorf("natsbus: object %s: %w", name, err)
		}
	}
	return nil
}

// PruneModule deletes module's objects whose digest is not in keep.
func (b *Bus) PruneModule(ctx context.Context, module string, keep ...string) error {
	if err := bus.CheckToken(module); err != nil {
		return err
	}
	obs, err := b.modulesStore(ctx)
	if err != nil {
		return err
	}
	infos, err := obs.List(ctx)
	if errors.Is(err, jetstream.ErrNoObjectsFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("natsbus: objects: %w", err)
	}
	var errs []error
	for _, i := range infos {
		parts := strings.Split(i.Name, ".")
		if len(parts) != 3 || parts[0] != module || slices.Contains(keep, parts[1]) {
			continue
		}
		if err := obs.Delete(ctx, i.Name); err != nil && !errors.Is(err, jetstream.ErrObjectNotFound) {
			errs = append(errs, fmt.Errorf("natsbus: object %s: %w", i.Name, err))
		}
	}
	return errors.Join(errs...)
}

// GetModule reads a build's two files, each at most limit bytes. The Object
// Store checks each against the digest it keeps; the caller checks both
// against the digest in the desired key, which only the core writes.
func (b *Bus) GetModule(ctx context.Context, module, digest string, limit int64) (wasm, manifest []byte, err error) {
	obs, err := b.modulesStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	read := func(part string) ([]byte, error) {
		name, err := bus.ModuleObject(module, digest, part)
		if err != nil {
			return nil, err
		}
		info, err := obs.GetInfo(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("natsbus: object %s: %w", name, err)
		}
		if info.Size > uint64(limit) {
			return nil, ErrTooLarge
		}
		r, err := obs.Get(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("natsbus: object %s: %w", name, err)
		}
		defer func() { _ = r.Close() }()
		data, err := io.ReadAll(io.LimitReader(r, limit+1))
		switch {
		case err != nil:
			return nil, fmt.Errorf("natsbus: object %s: %w", name, err)
		case int64(len(data)) > limit:
			return nil, ErrTooLarge
		}
		return data, nil
	}
	if wasm, err = read(bus.PartWasm); err != nil {
		return nil, nil, err
	}
	if manifest, err = read(bus.PartManifest); err != nil {
		return nil, nil, err
	}
	return wasm, manifest, nil
}

// PutDesired writes what edge should run of module, unless the key holds
// value already: a write is a change the edge acts on.
func (b *Bus) PutDesired(ctx context.Context, edge, module string, value []byte) error {
	k, err := bus.Desired(edge, module)
	if err != nil {
		return err
	}
	return b.putChanged(ctx, k, value)
}

// PutActual writes what edge runs of module. The edge's user may write its
// own actual keys and read none, so it is a plain put.
func (b *Bus) PutActual(ctx context.Context, edge, module string, value []byte) error {
	k, err := bus.Actual(edge, module)
	if err != nil {
		return err
	}
	kv, err := b.deliveryKV(ctx)
	if err != nil {
		return err
	}
	if _, err := kv.Put(ctx, k, value); err != nil {
		return fmt.Errorf("natsbus: %s: %w", k, err)
	}
	return nil
}

func (b *Bus) putChanged(ctx context.Context, k string, value []byte) error {
	kv, err := b.deliveryKV(ctx)
	if err != nil {
		return err
	}
	if e, err := kv.Get(ctx, k); err == nil && bytes.Equal(e.Value(), value) {
		return nil
	} else if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("natsbus: %s: %w", k, err)
	}
	if _, err := kv.Put(ctx, k, value); err != nil {
		return fmt.Errorf("natsbus: %s: %w", k, err)
	}
	return nil
}

// DeleteDesired drops what edge should run of module: the edge takes the
// module out of service.
func (b *Bus) DeleteDesired(ctx context.Context, edge, module string) error {
	k, err := bus.Desired(edge, module)
	if err != nil {
		return err
	}
	kv, err := b.deliveryKV(ctx)
	if err != nil {
		return err
	}
	if err := kv.Delete(ctx, k); err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("natsbus: %s: %w", k, err)
	}
	return nil
}

// DesiredEdges are the edges that have a desired key of module.
func (b *Bus) DesiredEdges(ctx context.Context, module string) ([]string, error) {
	pattern, err := bus.DesiredOfModule(module)
	if err != nil {
		return nil, err
	}
	kv, err := b.deliveryKV(ctx)
	if err != nil {
		return nil, err
	}
	lister, err := kv.ListKeysFiltered(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("natsbus: %s: %w", pattern, err)
	}
	defer func() { _ = lister.Stop() }()
	var out []string
	for k := range lister.Keys() {
		if edge, _, ok := bus.ParseDesired(k); ok {
			out = append(out, edge)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Actual reads what edge says it runs of module; false when it has said
// nothing.
func (b *Bus) Actual(ctx context.Context, edge, module string) ([]byte, bool, error) {
	k, err := bus.Actual(edge, module)
	if err != nil {
		return nil, false, err
	}
	kv, err := b.deliveryKV(ctx)
	if err != nil {
		return nil, false, err
	}
	e, err := kv.Get(ctx, k)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("natsbus: %s: %w", k, err)
	}
	return e.Value(), true, nil
}

// DesiredUpdate is one desired key as an edge's watch sees it: the edge
// and the module from the key, never from the value.
type DesiredUpdate struct {
	Edge, Module string
	Value        []byte
	Deleted      bool
}

// WatchDesired calls fn with each of edge's desired keys as it stands, then
// with every change, until ctx ends or stop is called; the watch keeps
// going across a link that comes back. fn runs on one goroutine. A key
// that is not a desired key of one module is not passed on.
func (b *Bus) WatchDesired(ctx context.Context, edge string, fn func(DesiredUpdate)) (stop func() error, err error) {
	pattern, err := bus.DesiredOf(edge)
	if err != nil {
		return nil, err
	}
	kv, err := b.deliveryKV(ctx)
	if err != nil {
		return nil, err
	}
	w, err := kv.Watch(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("natsbus: watch %s: %w", pattern, err)
	}
	go func() {
		for e := range w.Updates() {
			if e == nil { // the keys as they stood are all in
				continue
			}
			e2, m, ok := bus.ParseDesired(e.Key())
			if !ok {
				b.log.Warn("natsbus: a delivery key that is not a desired key is skipped", "edge", edge)
				continue
			}
			fn(DesiredUpdate{Edge: e2, Module: m, Value: e.Value(),
				Deleted: e.Operation() == jetstream.KeyValueDelete || e.Operation() == jetstream.KeyValuePurge})
		}
	}()
	return w.Stop, nil
}
