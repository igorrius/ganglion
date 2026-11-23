package natsbus

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// Settings of what Ensure creates (R-BUS-2).
const (
	// cellHistory is how many values the cells bucket keeps per key.
	cellHistory = 64
	// streamMaxAge is how long a stream keeps a message.
	streamMaxAge = 7 * 24 * time.Hour
	// streamDuplicates is the window in which a repeated Nats-Msg-Id is
	// dropped.
	streamDuplicates = 2 * time.Minute
	// deliveryHistory is how many values the delivery bucket keeps per
	// key: what an edge was told and said lately, for the log of a change.
	deliveryHistory = 8
)

// streams are the streams the bus keeps, by name and subject.
var streams = []jetstream.StreamConfig{
	streamConfig(bus.StreamNotify, bus.SubjectsNotify),
	streamConfig(bus.StreamEvents, bus.SubjectsEvents),
}

func streamConfig(name, subjects string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:       name,
		Subjects:   []string{subjects},
		Storage:    jetstream.FileStorage,
		MaxAge:     streamMaxAge,
		Duplicates: streamDuplicates,
	}
}

// Ensure creates the cells bucket and the NOTIFY and EVENTS streams, or
// brings existing ones to these settings. It can run on every start.
func (b *Bus) Ensure(ctx context.Context) error {
	if b.nc == nil {
		return bus.ErrNotConnected
	}
	if _, err := b.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  bus.BucketCells,
		History: cellHistory,
		Storage: jetstream.FileStorage,
	}); err != nil {
		return fmt.Errorf("natsbus: bucket %s: %w", bus.BucketCells, err)
	}
	for _, cfg := range streams {
		if err := b.ensureStream(ctx, cfg); err != nil {
			return fmt.Errorf("natsbus: stream %s: %w", cfg.Name, err)
		}
	}
	if b.IsEdge() {
		// An edge reads the core's delivery bucket and files; a bucket of
		// its own would take the actual keys it writes for the core.
		return nil
	}
	return b.ensureDelivery(ctx)
}

// ensureDelivery creates the delivery bucket and the modules' Object Store
// (ADR-0117).
func (b *Bus) ensureDelivery(ctx context.Context) error {
	if _, err := b.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  bus.BucketDelivery,
		History: deliveryHistory,
		Storage: jetstream.FileStorage,
	}); err != nil {
		return fmt.Errorf("natsbus: bucket %s: %w", bus.BucketDelivery, err)
	}
	if _, err := b.js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:  bus.ObjectModules,
		Storage: jetstream.FileStorage,
	}); err != nil {
		return fmt.Errorf("natsbus: object store %s: %w", bus.ObjectModules, err)
	}
	return nil
}
