package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Errors of the store; each is a refusal that leaves the state and the
// active release as they were. The agent maps them to the owner's words.
var (
	ErrNotNewer   = errors.New("update: the release is not newer than the running version")
	ErrEnv        = errors.New("update: the release needs a newer image")
	ErrBusy       = errors.New("update: another install is running")
	ErrNoPrevious = errors.New("update: nothing to roll back to")
	ErrBadState   = errors.New("update: state.json is not readable")
)

// Names inside <data>/releases.
const (
	StateFile  = "state.json"
	LockFile   = ".lock"
	BeforeDB   = "before.db"
	incomingFx = ".incoming-"
	maxState   = 64 << 10
)

// Pending is a version that has been put in place but has not confirmed
// itself yet.
type Pending struct {
	Version  string    `json:"version"`
	Attempts int       `json:"attempts"`
	Since    time.Time `json:"since"`
}

// Fallback records that the launcher gave up on a version; the watch tells
// the admins and clears it.
type Fallback struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	RestoredDB bool      `json:"restored_db"`
	Reason     Reason    `json:"reason,omitempty"`
	At         time.Time `json:"at"`
}

// State is state.json: which release the node runs and where it stands.
// "" for Active or Previous is the binary of the image or package.
type State struct {
	Active    string    `json:"active"`
	Previous  string    `json:"previous"`
	Pending   *Pending  `json:"pending,omitempty"`
	Fallback  *Fallback `json:"fallback,omitempty"`
	Confirmed string    `json:"confirmed"`
}

// validate checks that every version in the state is a core version, so a
// value read from the file can never become a path.
func (s State) validate() error {
	vs := []string{s.Active, s.Previous, s.Confirmed}
	if s.Pending != nil {
		if s.Pending.Attempts < 0 {
			return fmt.Errorf("%w: attempts %d", ErrBadState, s.Pending.Attempts)
		}
		vs = append(vs, s.Pending.Version)
	}
	if s.Fallback != nil {
		vs = append(vs, s.Fallback.From, s.Fallback.To)
		if !s.Fallback.Reason.valid() {
			return fmt.Errorf("%w: fallback reason %q", ErrBadState, s.Fallback.Reason)
		}
	}
	for _, v := range vs {
		if v == "" {
			continue
		}
		if _, err := ParseVersion(v); err != nil {
			return fmt.Errorf("%w: %w", ErrBadState, err)
		}
	}
	return nil
}

// NowFunc is the clock.
type NowFunc func() time.Time

// CopyDBFunc writes a consistent copy of the node's database to dst, a
// path that does not exist yet (VACUUM INTO, wired in config).
type CopyDBFunc func(ctx context.Context, dst string) error

// Store is the node's folder of core releases, <data>/releases, with its
// state and lock.
type Store struct {
	dir      string
	now      NowFunc
	platform string
	imageEnv int
	keys     []Key
	copyDB   CopyDBFunc
	log      *slog.Logger
	tel      telemetry
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithClock sets the clock.
func WithClock(now NowFunc) StoreOption { return func(s *Store) { s.now = now } }

// WithPlatform sets the platform, "goos/goarch", of the binaries to take.
func WithPlatform(p string) StoreOption { return func(s *Store) { s.platform = p } }

// WithImageEnv sets the level of the image's environment; 0 means the node
// has no image and the check is skipped.
func WithImageEnv(level int) StoreOption { return func(s *Store) { s.imageEnv = level } }

// WithKeys replaces the embedded release keys (tests).
func WithKeys(keys []Key) StoreOption { return func(s *Store) { s.keys = keys } }

// WithCopyDB sets the database copier; the default copies nothing, so no
// before.db is made.
func WithCopyDB(f CopyDBFunc) StoreOption { return func(s *Store) { s.copyDB = f } }

// WithLogger sets the logger; the default discards.
func WithLogger(l *slog.Logger) StoreOption { return func(s *Store) { s.log = l } }

func noCopy(context.Context, string) error { return nil }

// DirFor is the folder of the core releases of the node whose database is
// the file at db: releases/ beside it. ok is false for a database that is
// not a file, which keeps no releases.
func DirFor(db string) (dir string, ok bool) {
	if db == "" || db == ":memory:" {
		return "", false
	}
	return filepath.Join(filepath.Dir(db), "releases"), true
}

// HasState reports whether the store in dir has a state.json, which a node
// that never updated has not. Nothing is created.
func HasState(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, StateFile))
	return err == nil
}

// Open opens the store in dir, creating it (mode 0700) when it is not
// there.
func Open(dir string, opts ...StoreOption) (*Store, error) {
	s := &Store{dir: dir, now: time.Now, platform: runtime.GOOS + "/" + runtime.GOARCH, copyDB: noCopy,
		log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(s)
	}
	s.tel = newTelemetry()
	if s.keys == nil {
		keys, err := Keys()
		if err != nil {
			return nil, fmt.Errorf("update: keys: %w", err)
		}
		s.keys = keys
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return s, nil
}

// Dir is the store's folder.
func (s *Store) Dir() string { return s.dir }

// BinaryPath is where the binary of version is, for a version that
// ParseVersion accepts.
func (s *Store) BinaryPath(version string) string {
	return filepath.Join(s.dir, version, BinaryFile)
}

// State reads state.json; a store with none has the zero State. A file
// with an unknown field, a version that is not a core version or trailing
// data is ErrBadState.
func (s *Store) State() (State, error) {
	f, err := os.Open(filepath.Join(s.dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("update: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxState+1))
	if err != nil {
		return State{}, fmt.Errorf("update: %w", err)
	}
	return parseState(data)
}

func parseState(data []byte) (State, error) {
	if len(data) > maxState {
		return State{}, fmt.Errorf("%w: %d bytes", ErrBadState, len(data))
	}
	var st State
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrBadState, err)
	}
	if dec.More() {
		return State{}, fmt.Errorf("%w: trailing data", ErrBadState)
	}
	return st, st.validate()
}

// writeState writes state.json atomically: a temporary file in the same
// folder, fsync, rename, fsync of the folder.
func (s *Store) writeState(st State) (err error) {
	if err := st.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if err = os.Rename(tmp.Name(), filepath.Join(s.dir, StateFile)); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return syncDir(s.dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

// Installed is what an install put in place.
type Installed struct {
	Version  string
	Previous string
	Manifest Manifest
	Key      string // the name of the key that signed the release
}

// Install downloads, verifies and activates version, in this order; any
// refusal leaves the state and the active folder untouched (R-UPD-2…5):
//
//  1. the lock (ErrBusy);
//  2. the version is a core version newer than running (ErrNotNewer);
//  3. the release is fetched into a folder of its own;
//  4. the signature, the sums and release.json are verified;
//  5. the image's environment suffices (ErrEnv);
//  6. a copy of the database is made beside the release;
//  7. the folder takes the version's name and the state moves to it.
//
// Step 6 comes before the move so that a failed copy leaves nothing in
// place.
func (s *Store) Install(ctx context.Context, src Fetcher, version string, running Version) (Installed, error) {
	got, err := s.install(ctx, src, version, running)
	how := outcomeOf(err)
	s.tel.install(ctx, how)
	if err != nil {
		s.log.Warn("update: install refused", "version", version, "reason", string(how))
	} else {
		s.log.Info("update: installed", "version", got.Version, "previous", got.Previous)
	}
	return got, err
}

func (s *Store) install(ctx context.Context, src Fetcher, version string, running Version) (Installed, error) {
	unlock, err := lockDir(filepath.Join(s.dir, LockFile))
	if err != nil {
		return Installed{}, err
	}
	defer unlock()
	v, err := ParseVersion(version)
	if err != nil {
		return Installed{}, err
	}
	st, err := s.State()
	if err != nil {
		return Installed{}, err
	}
	if Compare(v, running) <= 0 || version == st.Active {
		return Installed{}, fmt.Errorf("%w: %s, running %s", ErrNotNewer, version, running)
	}
	rel, err := src.Get(ctx, version)
	if err != nil {
		return Installed{}, err
	}
	incoming, err := s.incoming()
	if err != nil {
		return Installed{}, err
	}
	defer func() { _ = os.RemoveAll(incoming) }() // a no-op once renamed
	got, err := s.stage(ctx, src, rel, incoming)
	if err != nil {
		return Installed{}, err
	}
	if err := s.place(st, incoming, &got); err != nil {
		return Installed{}, err
	}
	return got, nil
}

// incoming makes the folder a release is staged in.
func (s *Store) incoming() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	dir := filepath.Join(s.dir, incomingFx+hex.EncodeToString(b[:]))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return dir, nil
}

// stage fetches a release into dir and verifies everything in it.
func (s *Store) stage(ctx context.Context, src Fetcher, rel Release, dir string) (Installed, error) {
	if err := src.Fetch(ctx, rel, dir, s.platform); err != nil {
		return Installed{}, err
	}
	key, m, err := s.verify(dir, rel.Tag)
	if err != nil {
		return Installed{}, err
	}
	if s.imageEnv > 0 && m.Env > s.imageEnv {
		return Installed{}, fmt.Errorf("%w: needs level %d, the image has %d", ErrEnv, m.Env, s.imageEnv)
	}
	bin := filepath.Join(dir, BinaryFile)
	if err := os.Chmod(bin, 0o500); err != nil {
		return Installed{}, fmt.Errorf("update: %w", err)
	}
	if err := s.copyDB(ctx, filepath.Join(dir, BeforeDB)); err != nil {
		return Installed{}, fmt.Errorf("update: the database copy: %w", err)
	}
	return Installed{Version: rel.Tag, Manifest: m, Key: key.Name}, nil
}

// verify checks the signature over SHA256SUMS, the sums of the binary and
// release.json, and that release.json names tag.
func (s *Store) verify(dir, tag string) (Key, Manifest, error) {
	sums, err := readLimited(filepath.Join(dir, SumsFile), MaxSumsBytes)
	if err != nil {
		return Key{}, Manifest{}, err
	}
	sig, err := readLimited(filepath.Join(dir, SignatureFile), MaxSignatureBytes)
	if err != nil {
		return Key{}, Manifest{}, err
	}
	key, err := VerifySums(sums, sig, s.keys)
	if err != nil {
		return Key{}, Manifest{}, err
	}
	list, err := ParseSums(sums)
	if err != nil {
		return Key{}, Manifest{}, err
	}
	raw, err := readLimited(filepath.Join(dir, ManifestFile), MaxManifestBytes)
	if err != nil {
		return Key{}, Manifest{}, err
	}
	if err := list.Check(ManifestFile, bytes.NewReader(raw)); err != nil {
		return Key{}, Manifest{}, err
	}
	if err := s.checkBinary(list, dir); err != nil {
		return Key{}, Manifest{}, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return Key{}, Manifest{}, err
	}
	if m.Version != tag {
		return Key{}, Manifest{}, fmt.Errorf("%w: it names %s, not %s", ErrBadManifest, m.Version, tag)
	}
	return key, m, nil
}

func (s *Store) checkBinary(list Sums, dir string) error {
	goos, goarch, _ := strings.Cut(s.platform, "/")
	f, err := os.Open(filepath.Join(dir, BinaryFile))
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() { _ = f.Close() }()
	return list.Check(BinaryName(goos, goarch), f)
}

// readLimited reads a file that came from outside, at most limit bytes.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrIncomplete, filepath.Base(path), limit)
	}
	return b, nil
}

// place moves the staged folder to its version's name and makes it the
// active release.
func (s *Store) place(st State, incoming string, got *Installed) error {
	target := filepath.Join(s.dir, got.Version)
	if err := os.RemoveAll(target); err != nil { // a stale folder of the version
		return fmt.Errorf("update: %w", err)
	}
	if err := os.Rename(incoming, target); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	got.Previous = st.Active
	next := State{Active: got.Version, Previous: st.Active, Confirmed: st.Confirmed,
		Pending: &Pending{Version: got.Version, Since: s.now().UTC()}}
	if err := s.writeState(next); err != nil {
		// The folder is the release the state still names when the version is
		// the active or the previous one: it stays.
		if got.Version != st.Active && got.Version != st.Previous {
			_ = os.RemoveAll(target)
		}
		return err
	}
	s.prune(next)
	return nil
}

// Verify checks an installed release again, as the launcher does before it
// runs one: the signature over its SHA256SUMS, the sums of its binary and
// release.json, and that release.json names version. The files may have
// been changed since the install; nothing in the folder is trusted.
func (s *Store) Verify(version string) error {
	if _, err := ParseVersion(version); err != nil {
		return err
	}
	_, _, err := s.verify(filepath.Join(s.dir, version), version)
	return err
}

// Rollback makes the previous release the active one and the active one
// the previous, so a second rollback goes forward again. It returns the
// version moved to, "" for the binary of the image. ErrNoPrevious when
// there is nowhere to go, or the folder of the target is gone.
func (s *Store) Rollback(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	unlock, err := lockDir(filepath.Join(s.dir, LockFile))
	if err != nil {
		return "", err
	}
	defer unlock()
	st, err := s.State()
	if err != nil {
		return "", err
	}
	if st.Active == "" && st.Previous == "" {
		return "", ErrNoPrevious
	}
	if st.Previous != "" {
		if fi, err := os.Stat(s.BinaryPath(st.Previous)); err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s is not on disk", ErrNoPrevious, st.Previous)
		}
	}
	next := State{Active: st.Previous, Previous: st.Active, Confirmed: st.Confirmed}
	if st.Previous != "" { // the image's binary confirms at once
		next.Pending = &Pending{Version: st.Previous, Since: s.now().UTC()}
		// The target's before.db is from its own install, older than what
		// the node wrote since. Should the target fail its two starts, the
		// fall back must restore the database as it is now, not that one.
		if err := s.freshCopy(ctx, st.Previous); err != nil {
			return "", err
		}
	}
	if err := s.writeState(next); err != nil {
		return "", err
	}
	s.prune(next)
	s.log.Info("update: rolled back", "from", st.Active, "to", st.Previous)
	return st.Previous, nil
}

// freshCopy replaces version's before.db with a copy of the database as
// it is now: a temporary file, then a rename, so a failed copy keeps the
// old one.
func (s *Store) freshCopy(ctx context.Context, version string) error {
	dst := filepath.Join(s.dir, version, BeforeDB)
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	if err := s.copyDB(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("update: the database copy: %w", err)
	}
	if _, err := os.Stat(tmp); errors.Is(err, os.ErrNotExist) {
		return nil // a copier that copies nothing (no database wired)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

// mutate reads the state under the lock, lets change edit it and writes
// the result when change says it did edit it. It returns the state read.
func (s *Store) mutate(change func(*State) bool) (State, error) {
	unlock, err := lockDir(filepath.Join(s.dir, LockFile))
	if err != nil {
		return State{}, err
	}
	defer unlock()
	st, err := s.State()
	if err != nil {
		return State{}, err
	}
	next := st
	if !change(&next) {
		return st, nil
	}
	return st, s.writeState(next)
}

// prune removes every folder of a release that is neither active nor
// previous, and the leftovers of an interrupted install. The lock is held.
// It removes what it can: a folder that stays is tried again next time.
func (s *Store) prune(st State) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == st.Active || name == st.Previous {
			continue
		}
		if _, err := ParseVersion(name); err == nil || strings.HasPrefix(name, incomingFx) {
			_ = os.RemoveAll(filepath.Join(s.dir, name))
		}
	}
}
