package natsbus

import (
	"fmt"
	"time"

	"github.com/igorrius/ganglion/bus"
)

// Config is how a Bus opens. DefaultConfig gives the defaults; the caller
// overrides fields.
type Config struct {
	// Mode says whether the node runs the server or connects to one.
	Mode bus.Mode
	// StoreDir is the embedded server's JetStream folder. "" makes a private
	// temporary folder that Shutdown removes, for a node that keeps nothing
	// on disk. Unused for an external server.
	StoreDir string
	// URLs are the external server's addresses, nats:// or tls://; one
	// server or the members of a cluster. Unused for an embedded server.
	URLs []string
	// Credentials is the text of a .creds file for an external server; ""
	// connects without any. The text stays in memory.
	Credentials string
	// Chain is the node's trust chain (ADR-0109, ADR-0113). With one the
	// embedded server runs in operator mode, and an external server is sent
	// the account claims; the node connects as the core's user either way.
	// nil runs as before: no authentication on the embedded server, and
	// Credentials, if any, for an external one.
	Chain *Chain
	// ChainLocked says Chain is nil because the secret store is locked
	// (ADR-0113): the bus runs as before and /status says why.
	ChainLocked bool
	// WebSocket opens the edges' entry (ADR-0114): a WebSocket and a
	// leaf-node listener on loopback, served to the public listener by
	// WebSocketAddr. Only an embedded server with a chain opens it; without
	// a chain there is none (fail closed).
	WebSocket bool
	// MaxPayload is the largest message, and WebSocket frame, the embedded
	// server takes, in bytes.
	MaxPayload int32
	// MaxConnections is the embedded server's own limit on connections, a
	// backstop above the public proxy's cap on tunnels; the core's own
	// connection counts.
	MaxConnections int
	// AuthTimeout is how long a connection to the WebSocket listener may
	// stay silent before the server closes it.
	AuthTimeout time.Duration
	// Leaf makes the embedded server an edge's leaf node (ADR-0115): its own
	// JetStream domain, and a link to the core that comes and goes. nil is
	// the core's or a single node's bus. Only for the embedded server, and
	// not with a Chain: an edge's own server has one user, the edge.
	Leaf *Leaf
	// testListen opens a listener on a free loopback port, for the tests of
	// this package that must read the server's refusals. Nothing outside
	// the package can set it.
	testListen bool
	// testLeafPort opens a leaf-node listener on this loopback port, for
	// the tests that join an edge to this server; the link transport
	// itself is the public listener's (R-EDGE-4).
	testLeafPort int
	// StartTimeout is how long the embedded server may take to be ready,
	// and the connection to be made.
	StartTimeout time.Duration
	// MemoryStoreBytes is the embedded server's JetStream memory limit. The
	// bus keeps nothing in memory (file storage only), so it is small.
	MemoryStoreBytes int64
	// ReconnectMin and ReconnectMax bound the wait between reconnect
	// attempts to an external server, which doubles from the first to the
	// second.
	ReconnectMin, ReconnectMax time.Duration
}

// Leaf is how an edge's server links to the core.
type Leaf struct {
	// Domain is the edge's JetStream domain: the edge's name, so the core
	// reaches its streams as $JS.<Domain>.API.
	Domain string
	// URL is where the core's leaf listener is: nats-leaf://, ws:// or
	// wss://. "" runs the edge with no link, as an edge whose link is
	// down for good.
	URL string
	// CredsFile is the path of the edge's .creds file (its user JWT and
	// seed); the server reads it itself on every connect.
	CredsFile string
}

// DefaultConfig returns the embedded server storing under storeDir.
func DefaultConfig(storeDir string) Config {
	return Config{
		Mode:             bus.ModeEmbedded,
		StoreDir:         storeDir,
		StartTimeout:     10 * time.Second,
		MemoryStoreBytes: 1 << 20,
		MaxPayload:       1 << 20,
		MaxConnections:   128,
		AuthTimeout:      2 * time.Second,
		ReconnectMin:     500 * time.Millisecond,
		ReconnectMax:     30 * time.Second,
	}
}

// check refuses a Config that cannot open. An error never repeats the
// credentials.
func (c Config) check() error {
	switch c.Mode {
	case bus.ModeEmbedded:
		if len(c.URLs) > 0 || c.Credentials != "" {
			return fmt.Errorf("natsbus: urls and credentials are for an external server")
		}
	case bus.ModeExternal:
		if len(c.URLs) == 0 {
			return fmt.Errorf("natsbus: an external server needs urls")
		}
		for _, u := range c.URLs {
			if err := bus.CheckURL(u); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("natsbus: unknown mode %q", c.Mode)
	}
	if err := c.checkLeaf(); err != nil {
		return err
	}
	return c.checkChain()
}

// checkLeaf refuses a leaf setting the bus cannot run.
func (c Config) checkLeaf() error {
	l := c.Leaf
	switch {
	case l == nil:
		return nil
	case c.Mode != bus.ModeEmbedded:
		return fmt.Errorf("natsbus: a leaf node is the embedded server")
	case c.Chain != nil:
		return fmt.Errorf("natsbus: a leaf node runs without a trust chain: the core's chain does not leave the core")
	case bus.CheckEdgeName(l.Domain) != nil:
		return fmt.Errorf("natsbus: the leaf's domain must be the edge's name, one subject token and not %q", bus.CoreNode)
	case l.URL != "" && l.CredsFile == "":
		return fmt.Errorf("natsbus: a leaf link needs the edge's credentials file")
	}
	return nil
}
