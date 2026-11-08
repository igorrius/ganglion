package cmd

import (
	"context"
	"os"

	"github.com/igorrius/ganglion/sandbox"
	"github.com/urfave/cli/v3"
)

// sandboxCommand is the sandbox's helper (ADR-0039): the node starts its
// own binary as `ganglion __sandbox` for each command. Hidden: nobody types it.
func sandboxCommand() *cli.Command {
	return &cli.Command{
		Name:            sandbox.HelperArg,
		Hidden:          true,
		SkipFlagParsing: true,
		Action: func(context.Context, *cli.Command) error {
			os.Exit(sandbox.Helper()) // returns only when the command could not start
			return nil
		},
	}
}
