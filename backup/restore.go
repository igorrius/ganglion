package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxRestore caps what Restore writes: the sealed archive's plaintext and the
// files extracted from it, so a crafted archive cannot fill the disk.
const maxRestore int64 = 4 << 30

var (
	// ErrDestination means the restore target exists and is not an empty
	// directory: a restore never writes over a node's data (R-BAK-6).
	ErrDestination = errors.New("backup: the restore target must not exist or be an empty directory")
	// ErrUnsafe means the archive holds an entry that restore refuses: an
	// absolute or escaping name, a link, a device, a duplicate, or too much data.
	ErrUnsafe = errors.New("backup: the archive holds an entry that is refused")
	// ErrManifest means the archive's manifest is missing, unreadable or does
	// not match the files in the archive.
	ErrManifest = errors.New("backup: the archive does not match its manifest")
)

// topLevel are the archive's top-level names.
var topLevel = map[string]bool{dbName: true, settingsName: true, workspaceName: true, modulesName: true, busName: true}

// Restore opens the sealed archive under the master key and writes its
// contents into dst, which must not exist or be an empty directory
// (R-BAK-6). Nothing is extracted before every chunk of the archive
// authenticates: the stream is first decrypted into a temp file beside dst.
// Then the tar is extracted into a staging folder beside dst, refusing
// absolute names, "..", links, devices, duplicates and more than 4 GiB; every
// file must be in the manifest with the same SHA-256 and size and every
// listed file must be there; the database must pass PRAGMA integrity_check.
// Only then is the staging folder renamed to dst. On any failure the temp
// file and the staging folder are removed and dst is as it was. Folders are
// 0700 and files 0600 (0700 for a file the archive marked executable).
func Restore(ctx context.Context, archive io.Reader, key []byte, dst string) (m Manifest, err error) {
	dst = filepath.Clean(dst)
	if err := checkDestination(dst); err != nil {
		return Manifest{}, err
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("backup: restore: %w", err)
	}
	plain, err := decryptToTemp(ctx, archive, key, parent)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = os.Remove(plain) }()
	staging, err := os.MkdirTemp(parent, filepath.Base(dst)+".restore-")
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: restore: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)
		}
	}()
	if m, err = extract(ctx, plain, staging); err != nil {
		return Manifest{}, err
	}
	if err = integrityCheck(ctx, filepath.Join(staging, dbName)); err != nil {
		return Manifest{}, err
	}
	if err = install(staging, dst); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// checkDestination accepts a missing path or an empty directory.
func checkDestination(dst string) error {
	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("backup: restore: %w", err)
	case !info.IsDir():
		return ErrDestination
	}
	list, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("backup: restore: %w", err)
	}
	if len(list) > 0 {
		return ErrDestination
	}
	return nil
}

// install puts the staging folder in place of dst.
func install(staging, dst string) error {
	_, statErr := os.Lstat(dst)
	existed := statErr == nil
	if existed {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("backup: restore: %w", err)
		}
	}
	if err := os.Rename(staging, dst); err != nil {
		if existed {
			_ = os.Mkdir(dst, 0o700)
		}
		return fmt.Errorf("backup: restore: %w", err)
	}
	return nil
}

// decryptToTemp opens the sealed stream into a 0600 temp file in dir and
// returns its path; the file is removed on failure.
func decryptToTemp(ctx context.Context, archive io.Reader, key []byte, dir string) (name string, err error) {
	sr, err := NewReader(archive, key)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".restore-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("backup: restore: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("backup: restore: %w", cerr)
		}
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	n, err := io.Copy(f, io.LimitReader(&ctxReader{ctx: ctx, r: sr}, maxRestore+1))
	if err != nil {
		return "", err
	}
	if n > maxRestore {
		return "", fmt.Errorf("%w: more than %d bytes", ErrUnsafe, maxRestore)
	}
	return f.Name(), nil
}

// ctxReader stops a copy when ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// extract unpacks the tar.gz at file into staging and checks it against its
// manifest.
func extract(ctx context.Context, file, staging string) (Manifest, error) {
	f, err := os.Open(file)
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: restore: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: gzip: %w", ErrManifest, err)
	}
	tr := tar.NewReader(gz)
	m, err := readManifest(tr)
	if err != nil {
		return Manifest{}, err
	}
	x := &extractor{root: staging, m: m, seen: map[string]bool{}}
	for {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: tar: %w", ErrUnsafe, err)
		}
		if err := x.entry(hdr, tr); err != nil {
			return Manifest{}, err
		}
	}
	// Reading to the end makes gzip check its checksum.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return Manifest{}, fmt.Errorf("%w: gzip: %w", ErrManifest, err)
	}
	return m, x.finish()
}

// readManifest reads the first entry, which must be manifest.json.
func readManifest(tr *tar.Reader) (Manifest, error) {
	hdr, err := tr.Next()
	if err != nil || hdr.Name != manifestName || hdr.Typeflag != tar.TypeReg {
		return Manifest{}, fmt.Errorf("%w: the first entry is not manifest.json", ErrManifest)
	}
	var m Manifest
	dec := json.NewDecoder(io.LimitReader(tr, maxManifest))
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrManifest, err)
	}
	if m.Format != ManifestFormat {
		return Manifest{}, fmt.Errorf("%w: format %d is not known", ErrManifest, m.Format)
	}
	return m, nil
}

// extractor writes entries into root and tracks what it has seen.
type extractor struct {
	root  string
	m     Manifest
	seen  map[string]bool
	total int64
}

// entry checks and writes one tar entry.
func (x *extractor) entry(hdr *tar.Header, r io.Reader) error {
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
		return fmt.Errorf("%w: %q is not a file or a folder", ErrUnsafe, hdr.Name)
	}
	name, err := cleanName(hdr.Name)
	if err != nil {
		return err
	}
	if x.seen[name] {
		return fmt.Errorf("%w: %q appears twice", ErrUnsafe, name)
	}
	x.seen[name] = true
	if top, _, _ := strings.Cut(name, "/"); top == busName && name != busName && name != cellsName {
		return fmt.Errorf("%w: %q is not a part of the archive", ErrUnsafe, hdr.Name)
	}
	target := filepath.Join(x.root, filepath.FromSlash(restoredName(name)))
	// cleanName already refuses a name that leaves root; this second check
	// on the joined path keeps that true if cleanName ever changes.
	rel, err := filepath.Rel(x.root, target)
	if err != nil || strings.HasPrefix(rel, "..") || !filepath.IsLocal(rel) {
		return fmt.Errorf("%w: %q leaves the data directory", ErrUnsafe, hdr.Name)
	}
	if hdr.Typeflag == tar.TypeDir {
		if _, listed := x.m.Files[name]; listed {
			return fmt.Errorf("%w: %q is a folder but the manifest lists a file", ErrManifest, name)
		}
		if name == busName {
			return nil // its only file is written to the import folder
		}
		return os.MkdirAll(target, 0o700)
	}
	return x.file(name, target, hdr, r)
}

// restoredName is where an archive entry goes in the restored directory:
// the same place, except the cells export, which goes to the import folder
// the node's start reads (ImportPending) and not into the bus's store.
func restoredName(name string) string {
	if name == cellsName {
		return importDirName + "/" + importFileName
	}
	return name
}

// file writes a regular file and checks it against the manifest.
func (x *extractor) file(name, target string, hdr *tar.Header, r io.Reader) error {
	want, ok := x.m.Files[name]
	if !ok {
		return fmt.Errorf("%w: %q is not in the manifest", ErrManifest, name)
	}
	if hdr.Size < 0 || hdr.Size > maxRestore-x.total {
		return fmt.Errorf("%w: more than %d bytes", ErrUnsafe, maxRestore)
	}
	x.total += hdr.Size
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("backup: restore: %w", err)
	}
	mode := fs.FileMode(0o600)
	if hdr.Mode&0o100 != 0 {
		mode = 0o700
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("backup: restore: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("backup: restore %s: %w", name, err)
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%w: %q differs from its manifest entry", ErrManifest, name)
	}
	return nil
}

// finish checks that every listed file came and that the database did.
func (x *extractor) finish() error {
	for name := range x.m.Files {
		if !x.seen[name] {
			return fmt.Errorf("%w: %q is listed but missing", ErrManifest, name)
		}
	}
	if _, ok := x.m.Files[dbName]; !ok {
		return fmt.Errorf("%w: no database", ErrManifest)
	}
	return nil
}

// cleanName checks an entry name: relative, in canonical form (no "..", ".",
// empty or backslash component), under one of the archive's top-level names.
func cleanName(raw string) (string, error) {
	name := strings.TrimSuffix(raw, "/")
	bad := name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") ||
		path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || filepath.IsAbs(name)
	if bad || !topLevel[strings.SplitN(name, "/", 2)[0]] {
		return "", fmt.Errorf("%w: name %q", ErrUnsafe, raw)
	}
	return name, nil
}
