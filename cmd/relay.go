package cmd

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/igorrius/ganglion/mcp"
	"github.com/urfave/cli/v3"
)

// relayCommand is `ganglion mcp-relay --socket <path>`, hidden: the
// claude CLI starts it as its MCP server for one run, and it carries the
// CLI's stdio to the core's per-run socket, sending the token from the
// environment first (ADR-0036). It does nothing else and logs nothing:
// what it carries is the conversation's tool traffic. Its stdout is the
// MCP stream the CLI reads, so a usage error prints nothing there: it is
// returned, and main writes it to stderr.
func relayCommand(in io.Reader, out io.Writer) *cli.Command {
	var socket string
	return &cli.Command{
		Name:   "mcp-relay",
		Usage:  "carry one claude run's MCP stdio to the core's socket (started by the claude CLI provider)",
		Hidden: true,
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error {
			return err
		},
		Flags: []cli.Flag{&cli.StringFlag{Name: "socket", Usage: "the run's Unix socket", Required: true,
			Destination: &socket}},
		Action: func(ctx context.Context, _ *cli.Command) error {
			token := os.Getenv(mcp.TokenEnv)
			if token == "" {
				return errors.New("mcp-relay: no token in " + mcp.TokenEnv)
			}
			return mcp.Relay(ctx, socket, token, in, out)
		},
	}
}
