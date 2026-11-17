package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	workDirName = "backup-tmp"
	// ResultFile is the name, in the data directory, of the file that holds
	// the last backup's Result.
	ResultFile = "backup.json"
	lockName   = "backup.lock"
	keyTime    = "20060102T150405Z"
	keySuffix  = ".tar.gz.enc"
)

// WorkPath is the work folder of a node whose data directory is dataDir.
func WorkPath(dataDir string) string { return filepath.Join(dataDir, workDirName) }

// ResultPath is the file of the last backup's Result in dataDir.
func ResultPath(dataDir string) string { return filepath.Join(dataDir, ResultFile) }

// ErrNoResult means no backup has been recorded yet.
var ErrNoResult = errors.New("backup: no backup has been recorded")

// ErrBusy is a backup refused because another one is running on the same
// data directory; nothing is recorded for it.
var ErrBusy = errors.New("backup: another backup of this node is running")

// Uploader puts an object into the bucket. *Client is one; tests use a fake.
type Uploader interface {
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
}

// Result is the outcome of the last backup, kept in ResultFile for the
// status line and the alert (R-BAK-5).
type Result struct {
	Time   time.Time `json:"time"`
	Size   int64     `json:"size"`
	Key    string    `json:"key"`
	OK     bool      `json:"ok"`
	Reason string    `json:"reason"`
	// LastOK is the time of the last successful backup, carried through
	// later failures so "no success for 36 hours" can be told from the file.
	LastOK time.Time `json:"last_ok"`
	// Kind says in a closed set why a failed backup failed; Detail is a
	// short fixed-form addition (an HTTP status and the S3 error code). Both
	// are safe to show: neither holds an address, a path or a secret.
	Kind   Failure `json:"kind,omitempty"`
	Detail string  `json:"detail,omitempty"`
}

// Config says what a Backup archives and where it puts the object.
type Config struct {
	// DataDir holds the work folder and ResultFile.
	DataDir   string
	Workspace string
	Modules   string
	Settings  string
	// Prefix is the object key's folder; slashes around it are ignored and
	// an empty prefix puts the object at the bucket's top.
	Prefix string
	// Version is the node's version and Schema its database's schema
	// version (0 when unknown); both go to the manifest.
	Version string
	Schema  int
}

// DefaultConfig is the layout of a node's data directory.
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:   dataDir,
		Workspace: filepath.Join(dataDir, workspaceName),
		Modules:   filepath.Join(dataDir, modulesName),
		Settings:  filepath.Join(dataDir, settingsName),
	}
}

// Backup makes one backup at a time.
type Backup struct {
	db  *sql.DB
	up  Uploader
	key []byte
	cfg Config
	now func() time.Time
}

// Option configures a Backup.
type Option func(*Backup)

// WithBackupClock replaces the clock (tests).
func WithBackupClock(now func() time.Time) Option { return func(b *Backup) { b.now = now } }

// New makes a Backup of the live database db, sealed under the 32-byte
// master key and put through up.
func New(db *sql.DB, up Uploader, masterKey []byte, cfg Config, opts ...Option) *Backup {
	b := &Backup{db: db, up: up, key: masterKey, cfg: cfg, now: time.Now}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Run makes one backup: a checked copy of the database, the archive, the
// sealed archive, one upload (R-BAK-1 – R-BAK-3). It works in
// <DataDir>/backup-tmp, which is emptied before and removed after, whatever
// the outcome, and records the outcome in <DataDir>/backup.json. It returns
// the error of the failed step; a failure to record it is not hidden.
func (b *Backup) Run(ctx context.Context) (Result, error) {
	release, err := lockRun(b.cfg.DataDir)
	if err != nil {
		return Result{}, err
	}
	defer release()
	at := b.now().UTC().Truncate(time.Second)
	res := Result{Time: at, Key: b.objectKey(at)}
	size, err := b.make(ctx, res.Key, at)
	if err != nil {
		res.Key, res.Reason = "", err.Error()
		res.Kind, res.Detail = classify(err)
	} else {
		res.OK, res.Size, res.LastOK = true, size, at
	}
	if !res.OK {
		if prev, perr := Last(b.cfg.DataDir); perr == nil {
			res.LastOK = prev.LastOK
		}
	}
	if werr := writeResult(b.cfg.DataDir, res); werr != nil {
		return res, errors.Join(err, werr)
	}
	return res, err
}

// make does the work in the work folder and returns the sealed size.
func (b *Backup) make(ctx context.Context, key string, at time.Time) (size int64, err error) {
	work := filepath.Join(b.cfg.DataDir, workDirName)
	if err := os.RemoveAll(work); err != nil {
		return 0, fmt.Errorf("backup: work folder: %w", err)
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return 0, fmt.Errorf("backup: work folder: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(work); err == nil && rerr != nil {
			err = fmt.Errorf("backup: work folder: %w", rerr)
		}
	}()
	snap := filepath.Join(work, dbName)
	if err := Snapshot(ctx, b.db, snap); err != nil {
		return 0, err
	}
	tarball := filepath.Join(work, "archive.tar.gz")
	if err := b.pack(ctx, snap, tarball, at); err != nil {
		return 0, err
	}
	sealed := filepath.Join(work, "archive.tar.gz.enc")
	if err := seal(tarball, sealed, b.key); err != nil {
		return 0, err
	}
	return upload(ctx, b.up, key, sealed)
}

// pack writes the archive of the snapshot and the data directory to dst.
func (b *Backup) pack(ctx context.Context, snap, dst string, at time.Time) (err error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: archive file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("backup: archive file: %w", cerr)
		}
	}()
	_, err = Pack(ctx, f, Sources{DB: snap, Workspace: b.cfg.Workspace, Modules: b.cfg.Modules,
		Settings: b.cfg.Settings, Version: b.cfg.Version, Schema: b.cfg.Schema, Time: at})
	return err
}

// seal encrypts the file src into the file dst.
func seal(src, dst string, key []byte) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("backup: seal: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: seal: %w", err)
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("backup: seal: %w", cerr)
		}
	}()
	w, err := NewWriter(out, key)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		return fmt.Errorf("backup: seal: %w", err)
	}
	return w.Close()
}

// upload puts the file at path under key and returns its size.
func upload(ctx context.Context, up Uploader, key, path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("backup: upload: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("backup: upload: %w", err)
	}
	if err := up.Put(ctx, key, f, info.Size()); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// objectKey is <prefix>/ganglion-<UTC time>.tar.gz.enc, with no leading slash.
func (b *Backup) objectKey(at time.Time) string {
	name := "ganglion-" + at.UTC().Format(keyTime) + keySuffix
	prefix := strings.Trim(b.cfg.Prefix, "/")
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

// Last reads the last recorded Result from the data directory. It returns
// ErrNoResult when no backup has run yet.
func Last(dataDir string) (Result, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, ResultFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, ErrNoResult
	}
	if err != nil {
		return Result{}, fmt.Errorf("backup: read result: %w", err)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, fmt.Errorf("backup: read result: %w", err)
	}
	return r, nil
}

// writeResult records r atomically: a temp file, synced, renamed.
func writeResult(dataDir string, r Result) (err error) {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("backup: record result: %w", err)
	}
	f, err := os.CreateTemp(dataDir, ResultFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("backup: record result: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("backup: record result: %w", err)
	}
	if err = os.Rename(f.Name(), filepath.Join(dataDir, ResultFile)); err != nil {
		return fmt.Errorf("backup: record result: %w", err)
	}
	return nil
}
