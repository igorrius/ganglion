package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/backup"
	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/urfave/cli/v3"
)

// errBackupUsage is a wrong call of a backup command.
var errBackupUsage = errors.New("usage")

// errBackupOff is `backup now` on a node whose file has no backup section.
var errBackupOff = errors.New("backups are off: add a backup: section to ganglion.yaml")

// errNoKey is a command that needs the master key and was not given it.
var errNoKey = errors.New("no master key: set " + vault.KeyEnv + " (or " + vault.KeyEnv + "_FILE)")

// backupCommand is `ganglion backup`: the host's way to make a backup at
// once and to restore one (R-BAK-1, R-BAK-6). hc is the HTTP client to the
// bucket; nil takes the default, so only a test gives one.
func backupCommand(out io.Writer, hc *http.Client) *cli.Command {
	return &cli.Command{
		Name:  "backup",
		Usage: "make a backup now, or restore one into an empty directory",
		Description: "  ganglion backup now                       make one backup and put it in the bucket\n" +
			"  ganglion backup restore --data DIR FILE   open a downloaded archive into DIR\n\n" +
			"Both use the node's master key: " + vault.KeyEnv + " (or its _FILE), else the file " + vault.KeyFile + "\n" +
			"beside the database (for `now`) or the one named by --key-file (for `restore`).\n" +
			"Neither makes a key: a backup under a new key could not be opened.",
		Commands: []*cli.Command{backupNow(out, hc), backupRestore(out)},
	}
}

func backupNow(out io.Writer, hc *http.Client) *cli.Command {
	var v flags.Run
	return &cli.Command{
		Name:  "now",
		Usage: "make one backup and put it in the bucket the backup: section names",
		Flags: []cli.Flag{flags.ConfigFlag(&v.Config), flags.DBFlag(&v.DB)},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("%w: ganglion backup now", errBackupUsage)
			}
			return runBackupNow(ctx, out, v, hc)
		},
	}
}

// runBackupNow makes one backup of the database at v.DB as the node would,
// beside a node that may be running: it takes no lock, as `secret set` does.
func runBackupNow(ctx context.Context, out io.Writer, v flags.Run, hc *http.Client) error {
	if _, err := os.Stat(v.DB); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no database at %s; set GANGLION_DB or --db to the node's database", v.DB)
	}
	src := secrets.OSEnv()
	b, err := bootstrap(ctx, v, src, os.Getenv, os.ReadFile)
	if err != nil {
		return err
	}
	if b.Settings.Backup == nil {
		return errBackupOff
	}
	key, err := existingKey(src, "", vault.KeyPath(v.DB))
	if err != nil {
		return err
	}
	conn, err := sqlitedb.Open(ctx, v.DB)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	bk, err := config.BackupFor(b, conn, key, hc)
	if err != nil {
		return err
	}
	res, err := bk.Run(ctx)
	if err != nil {
		return fmt.Errorf("the backup failed: %w", err)
	}
	_, err = fmt.Fprintf(out, "backup made: %s, %d bytes\n", res.Key, res.Size)
	return err
}

// existingKey is the master key the operator gave: the environment's, else
// the file named by keyFile, else the one at fallback. It never makes a key,
// which vault.LoadKey does for a file that is missing.
func existingKey(src secrets.Source, keyFile, fallback string) (vault.Key, error) {
	if keyFile != "" {
		return fileKey(keyFile)
	}
	text, err := src.Secret(vault.KeyEnv)
	switch {
	case err != nil:
		return vault.Key{}, fmt.Errorf("%w: %w", vault.ErrKey, err)
	case text != "":
		key, _, err := vault.LoadKey(src, "")
		return key, err
	case fallback == "":
		return vault.Key{}, errNoKey
	}
	return fileKey(fallback)
}

// fileKey is the key in the file at path, which must exist.
func fileKey(path string) (vault.Key, error) {
	if _, err := os.Stat(path); err != nil {
		return vault.Key{}, fmt.Errorf("%w: %w", vault.ErrKey, err)
	}
	key, _, err := vault.LoadKey(secrets.None{}, path)
	return key, err
}

func backupRestore(out io.Writer) *cli.Command {
	var data, keyFile string
	return &cli.Command{
		Name:      "restore",
		Usage:     "open a backup archive into an empty or missing directory",
		ArgsUsage: "ARCHIVE",
		Description: "The archive is the .tar.gz.enc file downloaded from the bucket with the provider's own tool.\n" +
			"Nothing is written unless the whole archive opens under the key and passes its checks.\n" +
			"The directory must not exist or be empty. Afterwards put the master key in it as " + vault.KeyFile + "\n" +
			"(or set " + vault.KeyEnv + ") and start the node on " + backupDBName + " there.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "data", Usage: "the directory to restore into", Required: true, Local: true, Destination: &data, TakesFile: true},
			&cli.StringFlag{Name: "key-file", Usage: "the master key's file (32 raw bytes, as " + vault.KeyFile + "); default: " + vault.KeyEnv + " or its _FILE",
				Local: true, Destination: &keyFile, TakesFile: true},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) != 1 {
				return fmt.Errorf("%w: ganglion backup restore --data DIR [--key-file FILE] ARCHIVE", errBackupUsage)
			}
			return runRestore(ctx, out, args[0], data, keyFile)
		},
	}
}

// backupDBName is the database's file name inside an archive.
const backupDBName = "ganglion.db"

// runRestore restores the archive at path into dir.
func runRestore(ctx context.Context, out io.Writer, path, dir, keyFile string) error {
	key, err := existingKey(secrets.OSEnv(), keyFile, "")
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	m, err := backup.Restore(ctx, f, key[:], dir)
	if err != nil {
		return fmt.Errorf("nothing was restored: %w", err)
	}
	_, err = fmt.Fprintf(out, "restored %d files into %s: a backup made %s by %s\n"+
		"put the master key there as %s (or set %s), then start the node on %s\n",
		len(m.Files), dir, m.Time.UTC().Format(time.DateTime+" UTC"), m.Version,
		vault.KeyFile, vault.KeyEnv, filepath.Join(dir, backupDBName))
	return err
}
