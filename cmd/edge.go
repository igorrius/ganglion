package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/edge"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/urfave/cli/v3"
)

const (
	// edgeCredsName is the credentials file beside the database when
	// --credentials names none (the same default as edge.credentials).
	edgeCredsName = "edge.creds"
	// edgeOperatorName is the operator's public key, written beside the
	// credentials.
	edgeOperatorName = "edge.operator"
	// joinTimeout bounds the call to the node.
	joinTimeout = 30 * time.Second
	// maxCodeInput bounds a code read from standard input.
	maxCodeInput = 256
)

var errEdgeUsage = errors.New("usage")

// edgeCommand is `ganglion edge`: the edge's side of connecting a computer to
// a node (ADR-0116).
func edgeCommand(in io.Reader, out io.Writer) *cli.Command {
	return &cli.Command{
		Name:     "edge",
		Usage:    "connect this computer to a node as an edge",
		Commands: []*cli.Command{edgeJoin(in, out)},
	}
}

func edgeJoin(in io.Reader, out io.Writer) *cli.Command {
	var db, creds string
	var force bool
	return &cli.Command{
		Name:      "join",
		Usage:     "join a node with the one-time code its owner got in the chat",
		ArgsUsage: "URL CODE",
		Description: "URL is the node's public address, such as https://example.com. CODE is the one-time code\n" +
			"from the node's chat; `-` reads it from standard input, so it stays out of the shell history.\n" +
			"The command makes this edge's key, trades the code for its credentials and writes them\n" +
			"beside the database (0600). It prints what to put in ganglion.yaml and the command to run.",
		Flags: []cli.Flag{
			flags.DBFlag(&db),
			&cli.StringFlag{Name: "credentials", Usage: "where to write the credentials (default: edge.creds beside the database)", Local: true, Destination: &creds, TakesFile: true},
			&cli.BoolFlag{Name: "force", Usage: "replace credentials that exist already", Local: true, Destination: &force},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) != 2 {
				return fmt.Errorf("%w: ganglion edge join [--db FILE] [--credentials FILE] [--force] URL CODE", errEdgeUsage)
			}
			file := creds
			if file == "" {
				file = filepath.Join(filepath.Dir(db), edgeCredsName)
			}
			return runEdgeJoin(ctx, in, out, edgeJoinArgs{db: db, file: file, force: force, node: args[0], code: args[1]})
		},
	}
}

type edgeJoinArgs struct {
	db, file, node, code string
	force                bool
}

func runEdgeJoin(ctx context.Context, in io.Reader, out io.Writer, a edgeJoinArgs) error {
	// Before the code is spent: a file that would not be written must not
	// cost the owner a code.
	if _, err := os.Stat(a.file); err == nil && !a.force {
		return fmt.Errorf("%s exists already: it holds the key of an edge that joined; use --force to replace it (the old edge is then no longer reachable here)", a.file)
	}
	code, err := edgeCode(in, a.code)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	c, err := edgejoin.Join(ctx, &http.Client{Timeout: joinTimeout}, a.node, code)
	if err != nil {
		return err
	}
	if err := edge.WriteFileAtomic(filepath.Join(filepath.Dir(a.file), edgeOperatorName), []byte(c.Operator+"\n"), 0o644); err != nil {
		return fmt.Errorf("write the operator key: %w", err)
	}
	if err := edge.WriteFileAtomic(a.file, c.Creds, 0o600); err != nil {
		return fmt.Errorf("write the credentials: %w", err)
	}
	return printJoined(out, a, c)
}

// edgeCode is the code from the argument, or from standard input for "-".
func edgeCode(in io.Reader, arg string) (string, error) {
	if arg != "-" {
		return strings.TrimSpace(arg), nil
	}
	if in == nil {
		return "", fmt.Errorf("%w: no standard input to read the code from", errEdgeUsage)
	}
	raw, err := io.ReadAll(io.LimitReader(in, maxCodeInput))
	if err != nil {
		return "", fmt.Errorf("read the code: %w", err)
	}
	code := strings.TrimSpace(string(raw))
	if code == "" {
		return "", fmt.Errorf("%w: the code on standard input is empty", errEdgeUsage)
	}
	return code, nil
}

// printJoined says what joined and what to do next. It shows no seed, JWT or
// code.
func printJoined(out io.Writer, a edgeJoinArgs, c edgejoin.Credentials) error {
	_, err := fmt.Fprintf(out, `Joined as edge %q. The credentials are in %s (mode 0600); keep them private.

Put this in ganglion.yaml beside the database:

edge:
  name: %s
  url: %s
`, c.Name, a.file, c.Name, c.BusURL)
	if err != nil {
		return err
	}
	if filepath.Base(a.file) != edgeCredsName || filepath.Dir(a.file) != filepath.Dir(a.db) {
		if _, err := fmt.Fprintf(out, "  credentials: %s\n", a.file); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "\nThen run the edge:\n\n  ganglion run --roles edge --db %s\n", a.db)
	return err
}
