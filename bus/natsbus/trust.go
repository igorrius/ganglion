package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// claimsUpdateSubject is where a server with a full resolver takes an
// account's claims, from a user of the system account.
const claimsUpdateSubject = "$SYS.REQ.CLAIMS.UPDATE"

// ErrNoChain is an operation on the trust chain of a bus opened without one.
var ErrNoChain = errors.New("natsbus: the bus has no trust chain")

// Chain is the trust chain the bus runs on; nil when it runs without one.
func (b *Bus) Chain() *Chain { return b.cfg.Chain }

// Revoke revokes the user key pub (R-EDGE-5): it is stored in the
// revocation list, and the account's claims that carry it reach the server
// at once, which drops the user's connections and refuses it from then on.
// Revoking a key twice does nothing the second time.
func (b *Bus) Revoke(ctx context.Context, pub string) error {
	c := b.cfg.Chain
	if c == nil {
		return ErrNoChain
	}
	c.publishing.Lock()
	defer c.publishing.Unlock()
	if _, err := c.revoke(ctx, pub); err != nil {
		return err
	}
	return b.publishClaims(ctx)
}

// publishClaims hands the server the current claims of NET: in memory to
// the embedded server, through the system account to an external one. The
// caller holds the chain's publishing lock.
func (b *Bus) publishClaims(ctx context.Context) error {
	if b.srv == nil {
		return pushLocked(ctx, b.cfg)
	}
	c := b.cfg.Chain
	tok, err := c.AccountJWT()
	if err != nil {
		return err
	}
	if res, ok := b.srv.AccountResolver().(*server.MemAccResolver); ok {
		if err := res.Store(c.AccountPublicKey(), tok); err != nil {
			return fmt.Errorf("natsbus: resolver: %w", err)
		}
	}
	acc, err := b.srv.LookupAccount(c.AccountPublicKey())
	if err != nil {
		return fmt.Errorf("natsbus: account: %w", err)
	}
	b.srv.UpdateAccountClaims(acc, c.accountClaims())
	return nil
}

// pushClaims connects to the external server as a short-lived user of the
// system account and pushes the claims of the system account and of NET
// (ADR-0113). The server must trust the chain's operator and run a full
// resolver; one that does not says so in the error.
func pushClaims(ctx context.Context, cfg Config) error {
	cfg.Chain.publishing.Lock()
	defer cfg.Chain.publishing.Unlock()
	return pushLocked(ctx, cfg)
}

// pushLocked is pushClaims under the chain's publishing lock.
func pushLocked(ctx context.Context, cfg Config) error {
	c := cfg.Chain
	creds, err := c.systemCreds()
	if err != nil {
		return err
	}
	nc, err := nats.Connect(strings.Join(cfg.URLs, ","),
		nats.Name("ganglion-push"),
		nats.Timeout(cfg.StartTimeout),
		nats.UserCredentialBytes(creds))
	if err != nil {
		return fmt.Errorf("natsbus: push account claims: connect as the system account: %w", err)
	}
	defer nc.Close()
	sys, err := c.SystemAccountJWT()
	if err != nil {
		return err
	}
	acc, err := c.AccountJWT()
	if err != nil {
		return err
	}
	for _, tok := range []string{sys, acc} {
		if err := pushOne(ctx, nc, tok, cfg); err != nil {
			return err
		}
	}
	return nil
}

// pushOne sends one account's claims and reads the server's answer.
func pushOne(ctx context.Context, nc *nats.Conn, tok string, cfg Config) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.StartTimeout)
	defer cancel()
	msg, err := nc.RequestWithContext(ctx, claimsUpdateSubject, []byte(tok))
	if err != nil {
		return fmt.Errorf("natsbus: push account claims: %w (does the server run resolver: full?)", err)
	}
	var resp server.ServerAPIClaimUpdateResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return fmt.Errorf("natsbus: push account claims: unreadable answer")
	}
	if resp.Error != nil {
		return fmt.Errorf("natsbus: push account claims: the server refused: %s", resp.Error.Description)
	}
	return nil
}

// checkChain refuses a chain the mode cannot use.
func (c Config) checkChain() error {
	if c.Chain == nil {
		return nil
	}
	if c.Mode == bus.ModeExternal && c.Credentials != "" {
		return fmt.Errorf("natsbus: an external server takes credentials or the node's trust chain, not both")
	}
	return nil
}

// WebSocketAddr is the loopback host:port of the edges' entry (ADR-0114),
// the address the public listener proxies `/bus` to. It is "" unless the
// embedded server runs with a chain and Config.WebSocket: with no chain,
// a locked store or an external server there is nothing to reach.
func (b *Bus) WebSocketAddr() string {
	if b.srv == nil || b.cfg.Chain == nil || !b.cfg.WebSocket {
		return ""
	}
	u, err := url.Parse(b.srv.WebsocketURL())
	if err != nil {
		return ""
	}
	return u.Host
}
