package update

import (
	"context"
	"fmt"
	"os"
	"time"
)

// InstallTimeout bounds a whole install: the downloads, the checks and the
// copy of the database.
const InstallTimeout = 5 * time.Minute

// Releases is where the updater finds the project's releases; Source is the
// real one.
type Releases interface {
	Fetcher
	// Latest is the newest stable release.
	Latest(ctx context.Context) (Release, error)
}

// StoreFunc opens the node's release store. The updater calls it only when
// it has something to write, so a node that never updated has no releases
// folder.
type StoreFunc func() (*Store, error)

// Status is what /update shows: the running version and the newest stable
// release. Of a release it holds only the version, the date and the page the
// node built from the tag (R-UPD-7).
type Status struct {
	// Running is the version the node runs; zero for a build without a
	// release version.
	Running Version
	// Dev says the build has no release version: it moves to the image once
	// before it can be updated (ADR-0081).
	Dev bool
	// Newest is the newest stable release; zero when it is not known (a dev
	// build that could not reach GitHub).
	Newest Release
	// Newer says Newest is newer than Running.
	Newer bool
	// Image says the node knows its image's environment, so the check that
	// a release fits the image runs.
	Image bool
	// CanRollback says there is a version to go back to; RollbackTo is it,
	// "" for the binary of the image.
	CanRollback bool
	RollbackTo  string
}

// Updater is what the agent updates the node with: the release source and
// the store, for the version this process runs.
type Updater struct {
	src     Releases
	dir     string
	open    StoreFunc
	running string
	image   bool
}

// UpdaterOption configures an Updater.
type UpdaterOption func(*Updater)

// WithImage says the node knows its image's environment.
func WithImage(known bool) UpdaterOption { return func(u *Updater) { u.image = known } }

// NewUpdater makes the updater of the node whose releases folder is dir and
// whose binary reports the version running.
func NewUpdater(src Releases, dir string, open StoreFunc, running string, opts ...UpdaterOption) *Updater {
	u := &Updater{src: src, dir: dir, open: open, running: running}
	for _, o := range opts {
		o(u)
	}
	return u
}

// Status reads the newest stable release. A build without a release version
// is a Status with Dev set, not an error; a release that cannot be read is
// the error, except for such a build, which only lacks the newest.
func (u *Updater) Status(ctx context.Context) (Status, error) {
	st := Status{Image: u.image}
	st.RollbackTo, st.CanRollback = u.RollbackTarget()
	running, err := Running(u.running)
	st.Dev = err != nil
	newest, lerr := u.src.Latest(ctx)
	switch {
	case st.Dev:
		st.Newest = newest // zero on an error: the text then names no version
		return st, nil
	case lerr != nil:
		return Status{}, lerr
	}
	st.Running, st.Newest = running, newest
	st.Newer = Compare(newest.Version, running) > 0
	return st, nil
}

// Install puts version in place; the caller restarts the node. It returns
// the version installed.
func (u *Updater) Install(ctx context.Context, version string) (string, error) {
	if _, err := ParseVersion(version); err != nil {
		return "", err
	}
	running, err := Running(u.running)
	if err != nil {
		return "", err
	}
	s, err := u.open()
	if err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()
	got, err := s.Install(ctx, u.src, version, running)
	return got.Version, err
}

// Rollback moves to the previous release, "" for the binary of the image;
// ErrNoPrevious for a node that never updated, without making the folder.
func (u *Updater) Rollback(ctx context.Context) (string, error) {
	if !HasState(u.dir) {
		return "", ErrNoPrevious
	}
	s, err := u.open()
	if err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return s.Rollback(ctx)
}

// RollbackTarget is the version a rollback would go to, "" for the binary of
// the image, and whether there is one. It only reads state.json and never
// makes the folder; an unreadable state or a previous release gone from the
// disk is no target.
func (u *Updater) RollbackTarget() (to string, ok bool) {
	if !HasState(u.dir) {
		return "", false
	}
	s, err := u.open()
	if err != nil {
		return "", false
	}
	st, err := s.State()
	if err != nil || (st.Active == "" && st.Previous == "") {
		return "", false
	}
	if st.Previous != "" {
		if fi, err := os.Stat(s.BinaryPath(st.Previous)); err != nil || !fi.Mode().IsRegular() {
			return "", false
		}
	}
	return st.Previous, true
}
