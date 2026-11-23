package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/node"
	"github.com/urfave/cli/v3"
)

// busCommand is `ganglion bus`: what the operator of an external NATS
// server needs from the node's trust chain (ADR-0113).
func busCommand(out io.Writer) *cli.Command {
	return &cli.Command{
		Name:     "bus",
		Usage:    "the node's message bus",
		Commands: []*cli.Command{busTrust(out)},
	}
}

// busTrust prints the part of a NATS server's configuration that makes it
// trust the node's chain: the operator's JWT, the system account and a full
// resolver preloaded with it. Everything printed is public: JWTs and public
// keys, never a seed. With no node running it holds the database's lock and
// makes the chain when the store has none; beside a running node, which owns
// the chain, it only reads it.
func busTrust(out io.Writer) *cli.Command {
	var db string
	return &cli.Command{
		Name:  "trust",
		Usage: "print the NATS server configuration that trusts this node's chain (for bus.trust: chain)",
		Description: "Put the printed lines in the external server's configuration, then set\n" +
			"bus: {server: external, urls: [...], trust: chain} in ganglion.yaml. The node pushes its\n" +
			"account to the server at every start. Run this as the node's user, beside the database.",
		Flags: []cli.Flag{flags.DBFlag(&db)},
		Action: func(ctx context.Context, _ *cli.Command) error {
			load := natsbus.LoadChain
			release, err := node.LockDB(db)
			switch {
			case errors.Is(err, node.ErrLocked):
				load = natsbus.ReadChain
			case err != nil:
				return err
			default:
				defer release()
			}
			v, done, err := openStore(ctx, db, true)
			if err != nil {
				return storeError(err)
			}
			defer done()
			c, err := load(ctx, v.Internal(), v.Locked, time.Now)
			if errors.Is(err, natsbus.ErrStoreLocked) {
				return fmt.Errorf("%w: %s", err, lockedHelp)
			} else if err != nil {
				return err
			}
			return printTrust(out, c)
		},
	}
}

// printTrust writes the server configuration for the chain c.
func printTrust(out io.Writer, c *natsbus.Chain) error {
	op, err := c.OperatorJWT()
	if err != nil {
		return err
	}
	sys, err := c.SystemAccountJWT()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, `# The trust chain of a ganglion node: public keys and JWTs only.
operator: %s
system_account: %s
resolver: {
  type: full
  dir: "./jwt"
  allow_delete: false
}
resolver_preload: {
  %s: %s
}
`, op, c.SystemAccountPublicKey(), c.SystemAccountPublicKey(), sys)
	return err
}
