package natsbus

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// healthTimeout bounds the read of the store use a gauge makes.
const healthTimeout = 2 * time.Second

// metrics are the bus's instruments under the ganglion/bus scope (R-OBS-8):
// the connection's state and the JetStream store use as gauges, the
// disconnects and the reconnects as counters.
type metrics struct {
	meter                   metric.Meter
	disconnects, reconnects metric.Int64Counter
	connected, storeBytes   metric.Int64ObservableGauge
	registration            metric.Registration
	publishes, consumes     metric.Int64Counter
	pending                 metric.Int64ObservableGauge
}

// Outcomes of a publish or a consume, the "outcome" attribute.
const (
	outcomeOK    = "ok"
	outcomeError = "error"
)

func newMetrics() (*metrics, error) {
	m := otel.Meter("ganglion/bus")
	var (
		ms  = &metrics{meter: m}
		err error
	)
	if ms.disconnects, err = m.Int64Counter("ganglion.bus.disconnects",
		metric.WithDescription("Times the connection to the bus's server was lost.")); err != nil {
		return nil, err
	}
	if ms.reconnects, err = m.Int64Counter("ganglion.bus.reconnects",
		metric.WithDescription("Times the connection to the bus's server was made again.")); err != nil {
		return nil, err
	}
	if ms.connected, err = m.Int64ObservableGauge("ganglion.bus.connected",
		metric.WithDescription("1 while the node is connected to the bus's server, by mode.")); err != nil {
		return nil, err
	}
	if ms.storeBytes, err = m.Int64ObservableGauge("ganglion.bus.store_bytes",
		metric.WithDescription("Bytes the JetStream store holds."), metric.WithUnit("By")); err != nil {
		return nil, err
	}
	if ms.publishes, err = m.Int64Counter("ganglion.bus.publishes",
		metric.WithDescription("Messages published to a stream, by outcome (ok, error).")); err != nil {
		return nil, err
	}
	if ms.consumes, err = m.Int64Counter("ganglion.bus.consumes",
		metric.WithDescription("Messages a consumer handled, by consumer and outcome (ok: acked, error: the handler or the ack failed and the message comes again).")); err != nil {
		return nil, err
	}
	ms.pending, err = m.Int64ObservableGauge("ganglion.bus.consumer_pending",
		metric.WithDescription("Messages a consumer has not acked yet (waiting or being handled), by consumer: its lag."))
	return ms, err
}

func (m *metrics) published(ctx context.Context, outcome string) {
	m.publishes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func (m *metrics) consumed(ctx context.Context, consumer, outcome string) {
	m.consumes.Add(ctx, 1, metric.WithAttributes(attribute.String("consumer", consumer), attribute.String("outcome", outcome)))
}

// watchLag registers the gauge of c's pending messages; the caller
// unregisters it when the consumer stops.
func (m *metrics) watchLag(c jetstream.Consumer, name string) (metric.Registration, error) {
	return m.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, healthTimeout)
		defer cancel()
		info, err := c.Info(ctx)
		if err != nil {
			return nil // no reading this time
		}
		o.ObserveInt64(m.pending, int64(info.NumPending)+int64(info.NumAckPending),
			metric.WithAttributes(attribute.String("consumer", name)))
		return nil
	}, m.pending)
}

func (m *metrics) disconnected(*nats.Conn, error) { m.disconnects.Add(context.Background(), 1) }

func (m *metrics) reconnected(*nats.Conn) { m.reconnects.Add(context.Background(), 1) }

// watch registers the gauges' callback, which reads b when the metrics are
// collected.
func (m *metrics) watch(b *Bus) {
	reg, err := m.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, healthTimeout)
		defer cancel()
		h := b.Health(ctx)
		mode := metric.WithAttributes(attribute.String("mode", string(h.Mode)))
		up := int64(0)
		if h.Connected {
			up = 1
		}
		o.ObserveInt64(m.connected, up, mode)
		o.ObserveInt64(m.storeBytes, int64(h.StoreBytes), mode)
		return nil
	}, m.connected, m.storeBytes)
	if err != nil {
		b.log.Warn("natsbus: gauges not registered", "err", err)
		return
	}
	m.registration = reg
}

// stop ends the gauges' callback, so a closed bus is not read.
func (m *metrics) stop() {
	if m.registration != nil {
		_ = m.registration.Unregister()
		m.registration = nil
	}
}
