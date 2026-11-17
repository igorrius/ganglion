package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

// Archive paths (R-BAK-1). The manifest is the first entry of the tar.
const (
	manifestName  = "manifest.json"
	dbName        = "ganglion.db"
	settingsName  = "ganglion.yaml"
	workspaceName = "workspace"
	modulesName   = "modules"
	// templatesName is the top-level folder of modules/ that is left out:
	// it ships with the node and is not the operator's data.
	templatesName = "templates"
	// ManifestFormat is the version of the manifest and of the archive layout.
	ManifestFormat = 1
	// maxManifest bounds the manifest read back by Restore.
	maxManifest = 64 << 20
)

// ErrChanged means a file changed while the archive was being written, so
// the copy is not the file that was hashed: the backup fails (R-BAK-1).
var ErrChanged = errors.New("backup: a file changed while it was archived")

// FileInfo is what the manifest records about one archived file.
type FileInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is manifest.json, the first entry of the archive (R-BAK-1).
type Manifest struct {
	Format  int                 `json:"format"`
	Version string              `json:"version"`
	Time    time.Time           `json:"time"`
	Schema  int                 `json:"schema"`
	Files   map[string]FileInfo `json:"files"`
	// Skipped names the entries left out because they are neither a
	// regular file nor a directory (a symlink, a socket).
	Skipped []string `json:"skipped"`
}

// Sources says what Pack archives. DB is the snapshot file made by
// Snapshot; the others are paths in the node's data directory and may be
// missing, which leaves them out of the archive.
type Sources struct {
	DB        string
	Workspace string
	Modules   string
	Settings  string
	// Version is the node's version, Schema its database's schema version
	// (0 when unknown) and Time the backup's time; all go to the manifest.
	Version string
	Schema  int
	Time    time.Time
}

// entry is one archive entry to write.
type entry struct {
	name string
	disk string
	dir  bool
	exec bool
	mod  time.Time
}

// Snapshot makes a consistent copy of the live database db at dst (a file
// that does not exist yet) and fails unless the copy passes
// PRAGMA integrity_check: a damaged copy is a failed backup, never an
// upload (R-BAK-1).
func Snapshot(ctx context.Context, db *sql.DB, dst string) error {
	if err := sqlitedb.CopyTo(ctx, db, dst); err != nil {
		return err
	}
	return integrityCheck(ctx, dst)
}

// integrityCheck runs PRAGMA integrity_check on the database file at path;
// the single answer must be "ok". It leaves no journal files beside it.
func integrityCheck(ctx context.Context, path string) (err error) {
	db, err := sqlitedb.OpenReadOnly(ctx, path)
	if err != nil {
		return fmt.Errorf("backup: check %s: %w", filepath.Base(path), err)
	}
	defer func() {
		if cerr := db.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("backup: check close: %w", cerr)
		}
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
	}()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("backup: integrity check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var answers []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return fmt.Errorf("backup: integrity check: %w", err)
		}
		answers = append(answers, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("backup: integrity check: %w", err)
	}
	if len(answers) != 1 || answers[0] != "ok" {
		return fmt.Errorf("backup: integrity check failed: %s", strings.Join(answers, "; "))
	}
	return nil
}

// Pack writes the archive of src to w as tar+gzip, the manifest first, and
// returns the manifest. Only regular files and directories go in; anything
// else is listed in the manifest's Skipped. Every file is hashed before
// anything is written and hashed again while it is copied: a file that
// changed in between fails the backup with ErrChanged (R-BAK-1). Pack does
// not close w.
func Pack(ctx context.Context, w io.Writer, src Sources) (Manifest, error) {
	m := Manifest{Format: ManifestFormat, Version: src.Version, Time: src.Time.UTC().Truncate(time.Second),
		Schema: src.Schema, Files: map[string]FileInfo{}, Skipped: []string{}}
	entries, err := collect(src, &m)
	if err != nil {
		return Manifest{}, err
	}
	for _, e := range entries {
		if e.dir {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		fi, err := hashFile(e.disk)
		if err != nil {
			return Manifest{}, err
		}
		m.Files[e.name] = fi
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := writeArchive(ctx, tw, entries, m); err != nil {
		return Manifest{}, err
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, fmt.Errorf("backup: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, fmt.Errorf("backup: close gzip: %w", err)
	}
	return m, nil
}

// collect lists the entries of src in archive order and records in m what
// was skipped.
func collect(src Sources, m *Manifest) ([]entry, error) {
	if _, err := os.Lstat(src.DB); err != nil {
		return nil, fmt.Errorf("backup: database snapshot: %w", err)
	}
	entries := []entry{{name: dbName, disk: src.DB, mod: m.Time}}
	if e, ok, err := optionalFile(settingsName, src.Settings, m); err != nil {
		return nil, err
	} else if ok {
		entries = append(entries, e)
	}
	for _, r := range []struct{ name, disk, skipTop string }{
		{workspaceName, src.Workspace, ""},
		{modulesName, src.Modules, templatesName},
	} {
		walked, err := walkRoot(r.name, r.disk, r.skipTop, m)
		if err != nil {
			return nil, err
		}
		entries = append(entries, walked...)
	}
	return entries, nil
}

// optionalFile is the entry for a single file that may be missing.
func optionalFile(name, disk string, m *Manifest) (entry, bool, error) {
	info, err := os.Lstat(disk)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return entry{}, false, nil
	case err != nil:
		return entry{}, false, fmt.Errorf("backup: %s: %w", name, err)
	case !info.Mode().IsRegular():
		m.Skipped = append(m.Skipped, name)
		return entry{}, false, nil
	}
	return entry{name: name, disk: disk, exec: info.Mode()&0o100 != 0, mod: info.ModTime()}, true, nil
}

// walkRoot lists a folder under archive path name. A missing folder gives
// nothing; skipTop names a top-level child that is left out.
func walkRoot(name, root, skipTop string, m *Manifest) ([]entry, error) {
	var out []entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("backup: walk %s: %w", name, err)
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return fmt.Errorf("backup: walk %s: %w", name, rerr)
		}
		arch := name
		if rel != "." {
			arch = name + "/" + filepath.ToSlash(rel)
		}
		if skipTop != "" && rel == skipTop {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return addEntry(&out, arch, p, d, m)
	})
	return out, err
}

// addEntry appends a directory or regular file, and notes anything else.
func addEntry(out *[]entry, arch, disk string, d fs.DirEntry, m *Manifest) error {
	switch {
	case d.IsDir():
		*out = append(*out, entry{name: arch, disk: disk, dir: true, mod: m.Time})
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("backup: stat %s: %w", arch, err)
		}
		*out = append(*out, entry{name: arch, disk: disk, exec: info.Mode()&0o100 != 0, mod: info.ModTime()})
	default:
		m.Skipped = append(m.Skipped, arch)
	}
	return nil
}

// hashFile is the SHA-256 and the size of the file at path.
func hashFile(path string) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, fmt.Errorf("backup: hash: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return FileInfo{}, fmt.Errorf("backup: hash %s: %w", filepath.Base(path), err)
	}
	return FileInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// writeArchive writes the manifest and then every entry.
func writeArchive(ctx context.Context, tw *tar.Writer, entries []entry, m Manifest) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("backup: manifest: %w", err)
	}
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: manifestName, Mode: 0o600, Size: int64(len(body)), ModTime: m.Time}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: write manifest: %w", err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("backup: write manifest: %w", err)
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeEntry(tw, e, m); err != nil {
			return err
		}
	}
	return nil
}

// writeEntry writes one directory or file, checking the file against the
// hash taken before the write.
func writeEntry(tw *tar.Writer, e entry, m Manifest) error {
	if e.dir {
		hdr := &tar.Header{Typeflag: tar.TypeDir, Name: e.name + "/", Mode: 0o700, ModTime: e.mod}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("backup: write %s: %w", e.name, err)
		}
		return nil
	}
	want := m.Files[e.name]
	mode := int64(0o600)
	if e.exec {
		mode = 0o700
	}
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: e.name, Mode: mode, Size: want.Size, ModTime: e.mod}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: write %s: %w", e.name, err)
	}
	f, err := os.Open(e.disk)
	if err != nil {
		return fmt.Errorf("backup: open %s: %w", e.name, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	// One byte more than expected: a file that grew is seen, not cut.
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(f, want.Size+1))
	if err != nil {
		if errors.Is(err, tar.ErrWriteTooLong) {
			return fmt.Errorf("%w: %s", ErrChanged, e.name)
		}
		return fmt.Errorf("backup: write %s: %w", e.name, err)
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%w: %s", ErrChanged, e.name)
	}
	return nil
}
