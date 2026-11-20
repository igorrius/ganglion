package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Reason is why the launcher gave up on a release. The set is closed.
type Reason string

// The reasons a release is given up (R-UPD-4, R-UPD-5).
const (
	ReasonFailed     Reason = "failed"     // it started twice and did not confirm itself
	ReasonUnverified Reason = "unverified" // its files no longer verify
	ReasonNoExec     Reason = "noexec"     // the data volume does not allow running it
	ReasonExec       Reason = "exec"       // it could not be executed for another reason
)

func (r Reason) valid() bool {
	switch r {
	case "", ReasonFailed, ReasonUnverified, ReasonNoExec, ReasonExec:
		return true
	}
	return false
}

// Action is what the launcher does next.
type Action string

// The launcher's actions.
const (
	ActionRun      Action = "run"      // run this process: the floor, or already the active release
	ActionExec     Action = "exec"     // replace this process with the active release
	ActionFallBack Action = "fallback" // give up the active release
	ActionImage    Action = "image"    // run the image's binary, not older than the release, and drop the releases
)

// Decision is the launcher's next step; Reason is set for a fall back.
type Decision struct {
	Action Action
	Reason Reason
}

// maxAttempts is how many starts a version gets to confirm itself.
const maxAttempts = 2

// Decide is the launcher's choice at start, pure. self says that this
// process is the active release's own binary; image is this binary's
// version, the image's when it is not self. verify checks a release's
// files again (Store.Verify).
func Decide(st State, self bool, image string, verify func(version string) error) Decision {
	switch {
	case st.Active == "" || self:
		return Decision{Action: ActionRun}
	case ImageWins(image, st.Active):
		return Decision{Action: ActionImage}
	case st.Pending != nil && st.Pending.Attempts >= maxAttempts:
		return Decision{Action: ActionFallBack, Reason: ReasonFailed}
	case verify(st.Active) != nil:
		return Decision{Action: ActionFallBack, Reason: ReasonUnverified}
	}
	return Decision{Action: ActionExec}
}

// ImageWins reports whether the image's binary, of version image, is not
// older than the release active and runs instead of it (ADR-0102). A
// `git describe` version is its tag plus commits: it wins over a release
// at its tag or below. Any other version that is not a release, "dev"
// included, wins: someone deployed it on purpose.
func ImageWins(image, active string) bool {
	rel, err := ParseVersion(active)
	if err != nil {
		return true // never in a valid state: the image is the safe side
	}
	if v, err := Running(image); err == nil {
		return Compare(v, rel) >= 0
	}
	if tag, err := ParseVersion(describeTail.ReplaceAllString(image, "")); err == nil && describeTail.MatchString(image) {
		return Compare(tag, rel) >= 0
	}
	return true
}

// MigrationsFunc lists the migrations a database has applied, as
// "component:version" strings, from the database file at path. A database
// without a migrations table has none. The update package opens no
// database: cmd provides this over SQLite.
type MigrationsFunc func(ctx context.Context, path string) ([]string, error)

// ExecFunc replaces the process with the binary at path, the same
// arguments and environment. It returns only when that failed.
type ExecFunc func(path string) error

// Launcher is the step at the start of `run` that moves the process onto
// the active release or back (R-UPD-4, R-UPD-5).
type Launcher struct {
	Store      *Store
	DB         string // the live database file
	Self       string // this process's executable, symlinks resolved
	Image      string // this process's version (buildinfo)
	Migrations MigrationsFunc
	Exec       ExecFunc
	Log        io.Writer // where a line goes that the owner of the host should see
}

// errExeced marks an exec that returned without an error.
var errExeced = errors.New("update: exec returned")

// maxSteps bounds the launcher's loop: the active release, the previous
// one and the floor are the most it can visit.
const maxSteps = 4

// Launch decides and acts. It returns nil to run on as this process,
// which happens at the floor and when this process is the active release;
// when it executes another binary it does not return. An error means the
// state could not be read or written; the caller runs on as the floor.
//
// The store's lock is held while the state is read and written. When an
// install holds it the launcher does not wait: this process runs on, once.
func (l Launcher) Launch(ctx context.Context) error {
	unlock, err := lockDir(filepath.Join(l.Store.dir, LockFile))
	if errors.Is(err, ErrBusy) {
		_, _ = fmt.Fprintln(l.Log, "update: an install is running; starting this binary as it is, this time")
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock() // not reached on a successful exec: the lock closes with it
	st, err := l.Store.State()
	if err != nil {
		return err
	}
	for range maxSteps {
		d := Decide(st, l.isActive(st.Active), l.Image, l.Store.Verify)
		switch d.Action {
		case ActionRun:
			return nil
		case ActionImage:
			return l.dropReleases(st)
		case ActionExec:
			_, _ = fmt.Fprintf(l.Log, "update: the image's binary %s is older than the release %s; running %s\n",
				l.Image, st.Active, st.Active)
			if st, err = l.start(ctx, st); err != nil {
				if errors.Is(err, errExeced) {
					return nil
				}
				return err
			}
		case ActionFallBack:
			if st, err = l.fallBack(ctx, st, d.Reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// isActive reports whether this process is the binary of version.
func (l Launcher) isActive(version string) bool { return isBinaryOf(l.Store, version, l.Self) }

// start counts the attempt of a pending version, then executes the active
// binary. It returns only when the exec failed, with the state after the
// fall back that follows.
func (l Launcher) start(ctx context.Context, st State) (State, error) {
	if st.Pending != nil {
		next := st
		p := *st.Pending
		p.Attempts++
		next.Pending = &p
		if err := l.Store.writeState(next); err != nil {
			return st, err
		}
		st = next
	}
	err := l.Exec(l.Store.BinaryPath(st.Active))
	if err == nil {
		return st, errExeced // a real exec does not return; a test's stands in for it
	}
	reason := ReasonExec
	if errors.Is(err, fs.ErrPermission) {
		reason = ReasonNoExec
	}
	_, _ = fmt.Fprintf(l.Log, "update: %s could not be started: %v\n", st.Active, err)
	return l.fallBack(ctx, st, reason)
}

// dropReleases runs the image's binary in place of the active release: no
// release is active or previous any more, a pending one and an untold fall
// back go with them, and their folders are removed. The watch then tells
// the admins which version runs (ADR-0102).
func (l Launcher) dropReleases(st State) error {
	next := State{Confirmed: st.Confirmed}
	if err := l.Store.writeState(next); err != nil {
		return err
	}
	l.Store.prune(next)
	_, _ = fmt.Fprintf(l.Log, "update: the image's binary %s is not older than the release %s; running it and dropping the stored releases\n",
		l.Image, st.Active)
	return nil
}

// fallBack gives up the active release: the database is restored when
// that release migrated it, the previous release becomes the active one,
// and the state records it for the watch to tell the admins.
func (l Launcher) fallBack(ctx context.Context, st State, why Reason) (State, error) {
	restored, err := l.restoreDB(ctx, st.Active)
	if err != nil {
		_, _ = fmt.Fprintf(l.Log, "update: the database of %s could not be restored: %v\n", st.Active, err)
	}
	fb := &Fallback{From: st.Active, To: st.Previous, RestoredDB: restored, Reason: why, At: l.Store.now().UTC()}
	if st.Fallback != nil { // a chain of fall backs keeps its first release
		fb.From, fb.RestoredDB = st.Fallback.From, fb.RestoredDB || st.Fallback.RestoredDB
	}
	next := State{Active: st.Previous, Confirmed: st.Confirmed, Fallback: fb}
	if err := l.Store.writeState(next); err != nil {
		return st, err
	}
	l.Store.prune(next)
	_, _ = fmt.Fprintf(l.Log, "update: %s is given up (%s); back on %s\n", st.Active, why, versionName(st.Previous))
	return next, nil
}

func versionName(v string) string {
	if v == "" {
		return "the binary of the image"
	}
	return v
}

// restoreDB puts before.db of version over the live database when the
// version applied migrations the copy does not have. It reports whether it
// restored. A version with no copy, or no live database, restores nothing.
func (l Launcher) restoreDB(ctx context.Context, version string) (bool, error) {
	before := filepath.Join(l.Store.dir, version, BeforeDB)
	if version == "" || l.DB == "" {
		return false, nil
	}
	if _, err := os.Stat(before); err != nil {
		return false, nil //nolint:nilerr // no copy: nothing to restore from
	}
	if _, err := os.Stat(l.DB); err != nil {
		return false, nil //nolint:nilerr // no live database: nothing to replace
	}
	live, err := l.Migrations(ctx, l.DB)
	if err != nil {
		return false, err
	}
	was, err := l.Migrations(ctx, before)
	if err != nil {
		return false, err
	}
	if !migratedBeyond(live, was) {
		return false, nil
	}
	if err := copyOver(before, l.DB); err != nil {
		return false, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(l.DB + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("update: %w", err)
		}
	}
	return true, nil
}

// migratedBeyond reports whether live has applied a migration that was
// has not: the failed version changed the schema.
func migratedBeyond(live, was []string) bool {
	return slices.ContainsFunc(live, func(m string) bool { return !slices.Contains(was, m) })
}

// copyOver replaces dst with a copy of src: a temporary file beside dst,
// fsync, rename.
func copyOver(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".restore-*")
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, in); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return syncDir(filepath.Dir(dst))
}

// CleanStop takes back the attempt a start counted when the node running
// version stops for a restart it was asked to make (/restart, a settings
// change): such a stop is not a failure to start (ADR-0046). Only the
// pending version's own process takes an attempt back, never below zero.
// A busy store is left alone: the attempt stays counted.
func (s *Store) CleanStop(version string) error {
	unlock, err := lockDir(filepath.Join(s.dir, LockFile))
	if err != nil {
		return err
	}
	defer unlock()
	st, err := s.State()
	if err != nil {
		return err
	}
	if st.Pending == nil || st.Pending.Version != version || st.Pending.Attempts == 0 {
		return nil
	}
	p := *st.Pending
	p.Attempts--
	st.Pending = &p
	return s.writeState(st)
}
