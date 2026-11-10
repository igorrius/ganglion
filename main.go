// Command ganglion runs a node of the ganglion agent network.
//
// One binary carries every role; `ganglion run --roles` picks which ones
// this process hosts (docs/spec.md, section 5.1). The command tree lives in
// package cmd (ADR-0029).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/igorrius/ganglion/cmd"
	"github.com/igorrius/ganglion/node"
)

// exitRestart is EX_TEMPFAIL: a supervisor that restarts on failure starts
// the node again (R-CFG-6).
const exitRestart = 75

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cmd.Execute(ctx, os.Args, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, node.ErrRestart) {
			os.Exit(exitRestart)
		}
		fmt.Fprintln(os.Stderr, "ganglion:", err)
		os.Exit(1)
	}
}
