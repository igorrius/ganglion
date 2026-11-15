package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/internal/ghfetch"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/update"
	"github.com/urfave/cli/v3"
)

// errUpdateUsage is a wrong call of an update command.
var errUpdateUsage = errors.New("usage")

// imageEnvLevel reads the image's environment level from the file at path.
// A path that is empty or a file that is not there is 0, no image.
func imageEnvLevel(path string, read func(string) ([]byte, error)) (int, error) {
	if path == "" {
		return 0, nil
	}
	data, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("image env: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("image env: %s does not hold a level", path)
	}
	return n, nil
}

// imageEnvOr0 is the level of the file at path for a node that starts: a
// file that cannot be read is no image, so a bad file never stops a node.
func imageEnvOr0(path string, read func(string) ([]byte, error)) int {
	n, _ := imageEnvLevel(path, read)
	return n
}

// updateTarget is what the update commands share: the database and the
// image's env file.
type updateTarget struct{ db, imageEnv string }

func (t *updateTarget) flags() []cli.Flag {
	return []cli.Flag{flags.DBFlag(&t.db), flags.ImageEnvFlag(&t.imageEnv)}
}

// updateCommand is `ganglion update`: the host's way to the node's core
// releases, beside the owner's /update (ADR-0081). It works on the files
// only: a node that runs is not stopped, and the new version starts when the
// node does. Every verification is the node's own.
func updateCommand(out io.Writer) *cli.Command {
	var t updateTarget
	return &cli.Command{
		Name:  "update",
		Usage: "show the node's core version and the newest release; install a release or go back to the previous one",
		Description: "  ganglion update                    the running version and the newest release\n" +
			"  ganglion update install vX.Y.Z     install that release; it starts when the node restarts\n" +
			"  ganglion update rollback           go back to the previous version\n\n" +
			"Run it as the node's user. A release is installed only when the project's signature checks.",
		Flags:    t.flags(),
		Commands: []*cli.Command{updateInstall(out), updateRollback(out)},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("%w: ganglion update [install vX.Y.Z | rollback]", errUpdateUsage)
			}
			u, err := t.updater()
			if err != nil {
				return err
			}
			return printUpdateStatus(ctx, out, u)
		},
	}
}

func updateInstall(out io.Writer) *cli.Command {
	var t updateTarget
	return &cli.Command{
		Name:      "install",
		Usage:     "install a release; the node starts it at its next restart",
		ArgsUsage: "vX.Y.Z",
		Flags:     t.flags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) != 1 {
				return fmt.Errorf("%w: ganglion update install vX.Y.Z", errUpdateUsage)
			}
			u, err := t.updater()
			if err != nil {
				return err
			}
			got, err := u.Install(ctx, args[0])
			if err != nil {
				return updateRefused(ctx, u, err)
			}
			_, err = fmt.Fprintf(out, "installed %s; restart the node to start it\n", got)
			return err
		},
	}
}

func updateRollback(out io.Writer) *cli.Command {
	var t updateTarget
	return &cli.Command{
		Name:  "rollback",
		Usage: "go back to the previous version; the node starts it at its next restart",
		Flags: t.flags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("%w: ganglion update rollback", errUpdateUsage)
			}
			u, err := t.updater()
			if err != nil {
				return err
			}
			to, err := u.Rollback(ctx)
			if err != nil {
				return updateRefused(ctx, u, err)
			}
			if to == "" {
				to = "the version of the image"
			}
			_, err = fmt.Fprintf(out, "back to %s; restart the node to start it\n", to)
			return err
		},
	}
}

// updater is the updater of the node whose database is at t.db: the
// project's GitHub releases and the releases folder beside the database,
// made when something is installed.
func (t *updateTarget) updater() (*update.Updater, error) {
	dir, ok := update.DirFor(t.db)
	if !ok {
		return nil, fmt.Errorf("%w: --db names no database file", errUpdateUsage)
	}
	level, err := imageEnvLevel(t.imageEnv, os.ReadFile)
	if err != nil {
		return nil, err
	}
	open := func() (*update.Store, error) {
		return update.Open(dir, update.WithImageEnv(level), update.WithCopyDB(copyDatabase(t.db)))
	}
	return update.NewUpdater(update.NewSource(ghfetch.New()), dir, open, runningVersion(dir), update.WithImage(level > 0)), nil
}

// runningVersion is the version the node runs: the active release of the
// folder when there is one (the launcher starts it), else this binary's. The
// operator's binary is the image's, which may be older than the release the
// node runs.
func runningVersion(dir string) string {
	own := buildinfo.Read().Version
	if !update.HasState(dir) {
		return own
	}
	s, err := update.Open(dir)
	if err != nil {
		return own
	}
	if st, err := s.State(); err == nil && st.Active != "" {
		return st.Active
	}
	return own
}

// copyDatabase copies the database at db for a release to fall back to,
// without the node's lock: SQLite's own locking lets it work beside a node.
func copyDatabase(db string) update.CopyDBFunc {
	return func(ctx context.Context, dst string) error {
		if _, err := os.Stat(db); err != nil {
			return fmt.Errorf("no database at %s; set GANGLION_DB or --db to the node's database", db)
		}
		conn, err := sqlitedb.Open(ctx, db)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		return sqlitedb.CopyTo(ctx, conn, dst)
	}
}

// printUpdateStatus writes the running version and the newest release.
func printUpdateStatus(ctx context.Context, out io.Writer, u *update.Updater) error {
	st, err := u.Status(ctx)
	if err != nil {
		return updateRefused(ctx, u, err)
	}
	if st.Dev {
		_, err = fmt.Fprintln(out, devBuildText(st))
		return err
	}
	_, _ = fmt.Fprintf(out, "running %s\nnewest %s, published %s: %s\n", st.Running, st.Newest.Version,
		st.Newest.Published.Format("2006-01-02"), st.Newest.Page)
	switch {
	case st.Newer:
		_, _ = fmt.Fprintf(out, "to move to it: ganglion update install %s\n", st.Newest.Version)
	default:
		_, _ = fmt.Fprintln(out, "the node is up to date")
	}
	if !st.Image {
		_, _ = fmt.Fprintln(out, english(texts.UpdateNoImage, nil))
	}
	return nil
}

// updateRefused is the error of an update that did not happen, in the node's
// own English words; the cause, which may hold what the network said, is not
// in it.
func updateRefused(ctx context.Context, u *update.Updater, err error) error {
	if errors.Is(err, update.ErrDevBuild) {
		st, _ := u.Status(ctx)
		return errors.New(devBuildText(st))
	}
	if key := update.RefusalKey(err); key != texts.UpdateRefusedOther {
		return errors.New(english(key, nil))
	}
	return fmt.Errorf("%s: %w", english(texts.UpdateRefusedOther, nil), err)
}

// devBuildText says a build without a release version moves to the image
// once, naming the image of the newest release when it is known.
func devBuildText(st update.Status) string {
	if st.Newest.Version.IsZero() {
		return english(texts.UpdateDevBuildAny, nil)
	}
	return english(texts.UpdateDevBuild, map[string]string{"image": update.ImageRef(st.Newest.Version)})
}

// english is the shipped English text of k with {name} replaced from vars.
func english(k texts.Key, vars map[string]string) string {
	s := texts.EnglishText(k)
	for name, v := range vars {
		s = strings.ReplaceAll(s, "{"+name+"}", v)
	}
	return s
}
