// Command ganglion runs a node of the ganglion agent network.
//
// One binary carries every role; --roles picks which ones this process
// hosts (docs/spec.md, section 5.1).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/igorrius/ganglion/node"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "ganglion:", err)
		os.Exit(1)
	}
}

// logLevel reads GANGLION_LOG_LEVEL: debug, info (default), warn or error.
func logLevel(s string) (slog.Level, error) {
	if s == "" {
		return slog.LevelInfo, nil
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("GANGLION_LOG_LEVEL=%q is not debug, info, warn or error", s)
	}
	return l, nil
}

// run parses args, then runs the node until ctx is cancelled. Logs and the
// version string go to out.
func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ganglion", flag.ContinueOnError)
	fs.SetOutput(out)
	rolesFlag := fs.String("roles", "core,edge,worker", "comma-separated roles to run: core, edge, worker")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		_, err := fmt.Fprintln(out, "ganglion", version)
		return err
	}

	roles, err := node.ParseRoles(*rolesFlag)
	if err != nil {
		return err
	}
	cfg, err := node.ConfigFromEnv(os.Getenv, os.ReadFile)
	if err != nil {
		return err
	}
	level, err := logLevel(os.Getenv("GANGLION_LOG_LEVEL"))
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})).With("version", version)
	return node.Run(ctx, roles, cfg, logger)
}
