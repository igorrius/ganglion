// Package natsbus is the one package that imports NATS (R-BUS-1). It opens
// the node's bus: either an embedded nats-server with JetStream on disk,
// reached in-process, or a connection to a server or cluster the operator
// runs. It creates the bucket and the streams of R-BUS-2 and reports the
// bus's health for /status. The names live in package bus; the packages
// that use the bus declare the small interfaces they need.
package natsbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Bus is an open connection to the node's bus, with the embedded server
// behind it when the mode is embedded.
type Bus struct {
	cfg     Config
	log     *slog.Logger
	srv     *server.Server // nil for an external server
	nc      *nats.Conn
	js      jetstream.JetStream
	tmpDir  string // a store folder this Bus made and removes
	metrics *metrics
}

// Open starts the embedded server or connects to the external one, as
// cfg says. A server that cannot start or be reached is an error; the bus
// is not usable until Ensure has run.
func Open(ctx context.Context, cfg Config, logger *slog.Logger) (*Bus, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	m, err := newMetrics()
	if err != nil {
		return nil, fmt.Errorf("natsbus: metrics: %w", err)
	}
	b := &Bus{cfg: cfg, log: logger, metrics: m}
	switch {
	case cfg.Mode == bus.ModeEmbedded:
		err = b.startServer(cfg)
	case cfg.Chain != nil:
		err = pushClaims(ctx, cfg)
	}
	if err == nil {
		err = b.connect()
	}
	if err != nil {
		_ = b.close(ctx)
		return nil, err
	}
	b.metrics.watch(b)
	return b, nil
}

// connect makes the client connection and the JetStream handle.
func (b *Bus) connect() error {
	opts := []nats.Option{
		nats.Name("ganglion"),
		nats.Timeout(b.cfg.StartTimeout),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(b.onDisconnect),
		nats.ReconnectHandler(b.onReconnect),
	}
	if b.cfg.Chain != nil {
		creds, err := b.cfg.Chain.coreCreds()
		if err != nil {
			return err
		}
		opts = append(opts, nats.UserCredentialBytes(creds))
	}
	url := ""
	if b.srv != nil {
		opts = append(opts, nats.InProcessServer(b.srv))
	} else {
		opts = append(opts, b.externalOptions()...)
		url = strings.Join(b.cfg.URLs, ",")
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return fmt.Errorf("natsbus: connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return fmt.Errorf("natsbus: jetstream: %w", err)
	}
	b.nc, b.js = nc, js
	return nil
}

// onDisconnect counts and logs a lost connection; the client reconnects on
// its own.
func (b *Bus) onDisconnect(nc *nats.Conn, err error) {
	if err == nil { // our own drain
		return
	}
	b.metrics.disconnected(nc, err)
	b.log.Warn("natsbus: disconnected", "mode", b.cfg.Mode, "err", err)
}

// onReconnect counts and logs a connection that was made again.
func (b *Bus) onReconnect(nc *nats.Conn) {
	b.metrics.reconnected(nc)
	b.log.Info("natsbus: reconnected", "mode", b.cfg.Mode)
}

// externalOptions are the options of a connection to a server we do not
// run: back-off between reconnects, forever, and the credentials from
// memory.
func (b *Bus) externalOptions() []nats.Option {
	wait := backoff.Policy{Min: b.cfg.ReconnectMin, Max: b.cfg.ReconnectMax}
	opts := []nats.Option{nats.CustomReconnectDelay(func(attempts int) time.Duration { return wait.Delay(attempts) })}
	if b.cfg.Credentials != "" {
		opts = append(opts, nats.UserCredentialBytes([]byte(b.cfg.Credentials)))
	}
	return opts
}

// Shutdown drains the client, then stops the embedded server and waits for
// it. A second call does nothing.
func (b *Bus) Shutdown(ctx context.Context) error {
	b.metrics.stop()
	return b.close(ctx)
}

// close releases what Open made, in reverse order.
func (b *Bus) close(ctx context.Context) error {
	var errs []error
	if b.nc != nil {
		errs = append(errs, drain(ctx, b.nc))
		b.nc = nil
	}
	if b.srv != nil {
		b.srv.Shutdown()
		b.srv.WaitForShutdown()
		b.srv = nil
	}
	if b.tmpDir != "" {
		errs = append(errs, os.RemoveAll(b.tmpDir))
		b.tmpDir = ""
	}
	return errors.Join(errs...)
}

// drain flushes and closes nc, giving up when ctx ends.
func drain(ctx context.Context, nc *nats.Conn) error {
	if err := nc.Drain(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
		nc.Close()
		return fmt.Errorf("natsbus: drain: %w", err)
	}
	for !nc.IsClosed() {
		select {
		case <-ctx.Done():
			nc.Close()
			return fmt.Errorf("natsbus: drain: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return nil
}

// Health is what /status shows of the bus.
type Health struct {
	Mode      bus.Mode
	Connected bool
	// StoreBytes is the JetStream store in use; 0 when it could not be read.
	StoreBytes uint64
}

// Health reports the connection and the store use. A store that cannot be
// read leaves StoreBytes 0; the connection state is still true.
func (b *Bus) Health(ctx context.Context) Health {
	h := Health{Mode: b.cfg.Mode, Connected: b.nc != nil && b.nc.IsConnected()}
	if !h.Connected {
		return h
	}
	if b.srv != nil {
		acc := b.srv.GlobalAccount()
		if c := b.cfg.Chain; c != nil {
			if a, err := b.srv.LookupAccount(c.AccountPublicKey()); err == nil {
				acc = a
			}
		}
		h.StoreBytes = acc.JetStreamUsage().Store
		return h
	}
	if info, err := b.js.AccountInfo(ctx); err == nil {
		h.StoreBytes = info.Store
	}
	return h
}

// JetStream is the JetStream handle, for the packages that keep cells and
// streams on the bus. It is the one place the NATS types leave this package.
func (b *Bus) JetStream() jetstream.JetStream { return b.js }

// Conn is the client connection, for plain NATS subjects.
func (b *Bus) Conn() *nats.Conn { return b.nc }
