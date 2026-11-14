package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/urfave/cli/v3"
)

// setBy is who a secret set from the command line was set by.
const setBy = "cli"

// lockedHelp says what a locked store means to the operator.
const lockedHelp = "the secret store is locked: its key does not fit the stored secrets; " +
	"give the node the key they were stored with, or send /secrets reset in the chat to drop them"

// errSecretUsage is a wrong call of a secret command.
var errSecretUsage = errors.New("usage")

// secretCommand is `ganglion secret`: the operator's way to put a secret
// into the node's store (ADR-0077). It works beside a running node and
// takes effect at the secret's next use, with no restart. It never prints
// a value and never takes one as an argument: argv shows in ps.
func secretCommand(in io.Reader, out io.Writer) *cli.Command {
	return &cli.Command{
		Name:  "secret",
		Usage: "set, remove and list the secrets the node stores, encrypted, in its database",
		Description: "A secret belongs to one module (--module) or to the node itself (no --module: a store:NAME of ganglion.yaml).\n" +
			"A module reads only its own. The value is read from standard input, never from an argument:\n\n" +
			"  printf %s \"$VALUE\" | ganglion secret set --module demo-reader DEMO_APP_ID\n" +
			"  ganglion secret set TELEGRAM_BOT_TOKEN < token.txt\n\n" +
			"The node reads the new value at its next use; nothing restarts. No command shows a value.\n" +
			"The key of the store is " + vault.KeyEnv + " (or its _FILE), else the file " + vault.KeyFile + " beside the database:\n" +
			"keep it with the database, and run this command as the node's user.",
		Commands: []*cli.Command{secretSet(in, out), secretRm(out), secretList(out)},
	}
}

// secretTarget is what the set and rm commands share: the database, the
// module and the secret's name.
type secretTarget struct{ db, module string }

func (t *secretTarget) flags() []cli.Flag {
	return []cli.Flag{flags.DBFlag(&t.db),
		&cli.StringFlag{Name: "module", Usage: "the module the secret belongs to (demo-reader or local/demo-reader); without it, the node's own",
			Local: true, Destination: &t.module}}
}

// scope is the scope the flags name.
func (t *secretTarget) scope() secrets.Scope {
	switch {
	case t.module == "":
		return secrets.ScopeNode
	case modstore.IsLocal(t.module):
		return secrets.ModuleScope(t.module)
	}
	return secrets.ModuleScope(modstore.LocalPrefix + t.module)
}

// name is the command's one argument, a secret's name.
func name(cmd *cli.Command) (string, error) {
	args := cmd.Args().Slice()
	switch {
	case len(args) != 1:
		return "", fmt.Errorf("%w: ganglion secret %s [--module <name>] NAME; the value is read from standard input, never from an argument", errSecretUsage, cmd.Name)
	case !secrets.ValidName(args[0]):
		return "", fmt.Errorf("%w: a secret's name starts with a letter or _ and holds letters, digits, _, . and -, at most 128", errSecretUsage)
	}
	return args[0], nil
}

func secretSet(in io.Reader, out io.Writer) *cli.Command {
	var t secretTarget
	return &cli.Command{
		Name:      "set",
		Usage:     "store a secret; its value is read from standard input",
		ArgsUsage: "NAME",
		Flags:     t.flags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			n, err := name(cmd)
			if err != nil {
				return err
			}
			value, err := readValue(in)
			if err != nil {
				return err
			}
			v, done, err := openStore(ctx, t.db, true)
			if err != nil {
				return err
			}
			defer done()
			if err := v.Set(ctx, t.scope(), n, value, setBy); err != nil {
				return storeError(err)
			}
			_, err = fmt.Fprintf(out, "%s: %s is set\n", t.scope(), n)
			return err
		},
	}
}

// readValue is the value on standard input, without the whitespace around
// it (a trailing newline of echo or of a file). A little more than the
// longest value is read, so an endless input does not hang the command.
func readValue(in io.Reader) (string, error) {
	const slack = 64 // room for a newline after the longest value
	raw, err := io.ReadAll(io.LimitReader(in, vault.MaxValue+slack))
	if err != nil {
		return "", fmt.Errorf("read the value from standard input: %w", err)
	}
	switch value := strings.TrimSpace(string(raw)); {
	case value == "":
		return "", fmt.Errorf("%w: no value on standard input", vault.ErrEmpty)
	case len(value) > vault.MaxValue:
		return "", vault.ErrTooLong
	default:
		return value, nil
	}
}

func secretRm(out io.Writer) *cli.Command {
	var t secretTarget
	return &cli.Command{
		Name:      "rm",
		Usage:     "remove a stored secret",
		ArgsUsage: "NAME",
		Flags:     t.flags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			n, err := name(cmd)
			if err != nil {
				return err
			}
			v, done, err := openStore(ctx, t.db, false)
			if err != nil {
				return err
			}
			defer done()
			removed, err := v.Remove(ctx, t.scope(), n)
			if err != nil {
				return err
			}
			text := "%s: %s is removed\n"
			if !removed {
				text = "%s: no secret %s\n"
			}
			_, err = fmt.Fprintf(out, text, t.scope(), n)
			return err
		},
	}
}

func secretList(out io.Writer) *cli.Command {
	var db string
	return &cli.Command{
		Name:  "list",
		Usage: "list the stored secrets: whose, the name, when and by whom each was set; never a value",
		Flags: []cli.Flag{flags.DBFlag(&db)},
		Action: func(ctx context.Context, _ *cli.Command) error {
			v, done, err := openStore(ctx, db, false)
			if err != nil {
				return err
			}
			defer done()
			entries, err := v.List(ctx)
			if err != nil {
				return err
			}
			return printEntries(out, entries, v.Locked())
		},
	}
}

// printEntries writes the list as a table, and what a locked store means.
func printEntries(out io.Writer, entries []vault.Entry, locked bool) error {
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(out, "no secrets are stored")
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Scope, e.Name, e.SetAt.Format(time.DateTime), e.By)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if locked {
		_, _ = fmt.Fprintln(out, lockedHelp)
	}
	return nil
}

// openStore opens the store in the database at db on the node's key. It
// does not take the node's lock (ADR-0019 keeps two cores off one file;
// SQLite's own locking makes a second writer of one table safe), so it
// works beside a running node. Only set makes a database that is not there:
// a secret may be stored before the node's first start.
func openStore(ctx context.Context, db string, create bool) (*vault.Vault, func(), error) {
	if _, err := os.Stat(db); errors.Is(err, fs.ErrNotExist) && !create {
		return nil, nil, fmt.Errorf("no database at %s; set GANGLION_DB or --db to the node's database", db)
	}
	key, _, err := vault.LoadKey(secrets.OSEnv(), vault.KeyPath(db))
	if err != nil {
		return nil, nil, err
	}
	conn, err := sqlitedb.Open(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	v, err := vault.Open(ctx, conn, key, time.Now)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return v, func() { _ = conn.Close() }, nil
}

// storeError says what a locked store means in place of the bare error.
func storeError(err error) error {
	if errors.Is(err, vault.ErrLocked) {
		return fmt.Errorf("%w: %s", vault.ErrLocked, lockedHelp)
	}
	return err
}
