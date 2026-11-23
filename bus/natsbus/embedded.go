package natsbus

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
)

// storeDirMode is the permission of the embedded server's store folder: the
// node's own user only.
const storeDirMode = 0o700

// globalAccount is the account a server without operator keys stores
// JetStream under.
const globalAccount = "$G"

// startServer runs the embedded server with JetStream on disk, no listener
// and no signal handling, and waits until it takes connections. With a
// chain it runs in operator mode (ADR-0113): it trusts the operator and
// knows NET and the system account from a memory resolver.
func (b *Bus) startServer(cfg Config) error {
	dir, err := b.storeDir(cfg)
	if err != nil {
		return err
	}
	opts := &server.Options{
		JetStream:          true,
		StoreDir:           dir,
		JetStreamMaxMemory: cfg.MemoryStoreBytes,
		DontListen:         true,
		NoSigs:             true,
	}
	if cfg.Chain != nil {
		if err := adoptGlobalStore(dir, cfg.Chain.AccountPublicKey()); err != nil {
			return err
		}
		if err := operatorOptions(opts, cfg.Chain); err != nil {
			return err
		}
	}
	srv, err := server.NewServer(opts)
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

// operatorOptions sets opts to trust the chain's operator and to resolve
// its two accounts from memory.
func operatorOptions(opts *server.Options, c *Chain) error {
	opJWT, err := c.OperatorJWT()
	if err != nil {
		return err
	}
	oc, err := jwt.DecodeOperatorClaims(opJWT)
	if err != nil {
		return fmt.Errorf("natsbus: operator claims: %w", err)
	}
	res := &server.MemAccResolver{}
	acc, err := c.AccountJWT()
	if err != nil {
		return err
	}
	sys, err := c.SystemAccountJWT()
	if err != nil {
		return err
	}
	if err := res.Store(c.AccountPublicKey(), acc); err != nil {
		return fmt.Errorf("natsbus: resolver: %w", err)
	}
	if err := res.Store(c.SystemAccountPublicKey(), sys); err != nil {
		return fmt.Errorf("natsbus: resolver: %w", err)
	}
	opts.TrustedOperators = []*jwt.OperatorClaims{oc}
	opts.AccountResolver = res
	opts.SystemAccount = c.SystemAccountPublicKey()
	return nil
}

// adoptGlobalStore moves the JetStream data a server without operator keys
// kept under $G to the folder of account, once (ADR-0113). When both
// folders exist nothing is touched and the start fails.
func adoptGlobalStore(dir, account string) error {
	js := filepath.Join(dir, server.JetStreamStoreDir)
	from, to := filepath.Join(js, globalAccount), filepath.Join(js, account)
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("natsbus: store folder: %w", err)
	}
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("natsbus: store folder: both %s and %s hold data; keep one", from, to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("natsbus: store folder: %w", err)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("natsbus: store folder: move to the network account: %w", err)
	}
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
