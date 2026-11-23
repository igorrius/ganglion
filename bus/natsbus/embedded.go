package natsbus

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
)

// storeDirMode is the permission of the embedded server's store folder: the
// node's own user only.
const storeDirMode = 0o700

// loopback is where the embedded server's listeners bind.
const loopback = "127.0.0.1"

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
		MaxPayload:         cfg.MaxPayload,
		MaxConn:            cfg.MaxConnections,
	}
	if cfg.testListen {
		opts.DontListen, opts.Host, opts.Port = false, loopback, -1
	}
	if cfg.testLeafPort != 0 {
		opts.LeafNode.Host, opts.LeafNode.Port = "127.0.0.1", cfg.testLeafPort
	}
	if cfg.Leaf != nil {
		if err := leafOptions(opts, cfg.Leaf); err != nil {
			return err
		}
	}
	if cfg.Chain != nil {
		if err := adoptGlobalStore(dir, cfg.Chain.AccountPublicKey()); err != nil {
			return err
		}
		if err := operatorOptions(opts, cfg.Chain); err != nil {
			return err
		}
		if cfg.WebSocket {
			webSocketOptions(opts, cfg)
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

// leafOptions makes opts an edge's leaf node (ADR-0115): the JetStream
// domain is the edge's name, and the remote is the core's leaf listener.
// The edge's events are not exported as plain messages: they travel only
// through the core's stream, which sources the edge's, so a message that
// arrives by both ways cannot be stored twice.
func leafOptions(opts *server.Options, l *Leaf) error {
	opts.JetStreamDomain = l.Domain
	if l.URL == "" {
		return nil
	}
	u, err := url.Parse(l.URL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("natsbus: the core's leaf address is not an address")
	}
	opts.LeafNode.Remotes = []*server.RemoteLeafOpts{{
		URLs:        []*url.URL{u},
		Credentials: l.CredsFile,
		DenyExports: []string{bus.SubjectsEvents},
	}}
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

// webSocketOptions opens the edges' entry (ADR-0114): a WebSocket listener
// and the leaf-node listener a leaf over WebSocket needs, both on a free
// loopback port and without TLS (it ends at the platform). Authentication
// is the operator mode's: a user JWT and the nonce signature, or a refusal;
// the timeout closes a connection that never sends them.
func webSocketOptions(opts *server.Options, cfg Config) {
	opts.Websocket = server.WebsocketOpts{
		Host: loopback, Port: -1, NoTLS: true,
		AuthTimeout: cfg.AuthTimeout.Seconds(), HandshakeTimeout: 5 * time.Second,
	}
	opts.LeafNode.Host, opts.LeafNode.Port = loopback, -1
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
