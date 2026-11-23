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

// DefaultConfig returns the embedded server storing under storeDir.
func DefaultConfig(storeDir string) Config {
	return Config{
		Mode:             bus.ModeEmbedded,
		StoreDir:         storeDir,
		StartTimeout:     10 * time.Second,
		MemoryStoreBytes: 1 << 20,
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
	return c.checkChain()
}
