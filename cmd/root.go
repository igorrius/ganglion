// Package cmd is ganglion's command line: the command tree on urfave/cli
// (ADR-0029). main.go at the repository root calls Execute.
package cmd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/node"
	"github.com/urfave/cli/v3"
)

// startFunc runs a node; tests replace config.Run to see what would start.
type startFunc func(ctx context.Context, roles []node.Role, b config.Bootstrap, logger *slog.Logger) error

// Execute runs the command line in args (os.Args) until ctx ends. Output
// and logs go to out.
func Execute(ctx context.Context, args []string, out io.Writer) error {
	return newRoot(out, config.Run).Run(ctx, args)
}

func newRoot(out io.Writer, start startFunc) *cli.Command {
	var root flags.Root
	return &cli.Command{
		Name:      "ganglion",
		Usage:     "a node of the ganglion agent network",
		Version:   buildinfo.Read().Short(),
		Writer:    out,
		ErrWriter: out,
		Flags:     root.Flags(),
		Commands:  []*cli.Command{runCommand(&root, out, start), versionCommand(out), relayCommand(os.Stdin, out)},
		// Without a command: help, and a failure, so a supervisor still
		// starting the command line from before `run` sees an error
		// instead of a clean exit.
		Action: func(_ context.Context, cmd *cli.Command) error {
			_ = cli.ShowRootCommandHelp(cmd)
			return errNoCommand
		},
	}
}

var errNoCommand = errors.New("no command; to run a node use `ganglion run --roles ...`")
