package natsbus

import (
	"fmt"
	"os"

	"github.com/nats-io/nats-server/v2/server"
)

// storeDirMode is the permission of the embedded server's store folder: the
// node's own user only.
const storeDirMode = 0o700

// startServer runs the embedded server with JetStream on disk, no listener
// and no signal handling, and waits until it takes connections.
func (b *Bus) startServer(cfg Config) error {
	dir, err := b.storeDir(cfg)
	if err != nil {
		return err
	}
	srv, err := server.NewServer(&server.Options{
		JetStream:          true,
		StoreDir:           dir,
		JetStreamMaxMemory: cfg.MemoryStoreBytes,
		DontListen:         true,
		NoSigs:             true,
	})
	if err != nil {
		return fmt.Errorf("natsbus: server: %w", err)
	}
	srv.SetLoggerV2(slogLogger{log: b.log}, false, false, false)
	go srv.Start()
	if !srv.ReadyForConnections(cfg.StartTimeout) {
		srv.Shutdown()
		srv.WaitForShutdown()
		return fmt.Errorf("natsbus: server: not ready after %s", cfg.StartTimeout)
	}
	b.srv = srv
	return nil
}

// storeDir makes the store folder, or a private temporary one for "".
func (b *Bus) storeDir(cfg Config) (string, error) {
	if cfg.StoreDir == "" {
		dir, err := os.MkdirTemp("", "ganglion-bus-")
		if err != nil {
			return "", fmt.Errorf("natsbus: store folder: %w", err)
		}
		b.tmpDir = dir
		return dir, nil
	}
	if err := os.MkdirAll(cfg.StoreDir, storeDirMode); err != nil {
		return "", fmt.Errorf("natsbus: store folder: %w", err)
	}
	return cfg.StoreDir, nil
}
