package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// skippedFolders are the top-level folders of a module that are not its
// source: cargo's output and a repository's own files. A build does not copy
// them and the digest does not cover them (ruling 7 of M4c).
var skippedFolders = map[string]bool{"target": true, ".git": true}

// SourceFile is a regular file of a module folder.
type SourceFile struct {
	Path string `json:"path"` // relative to the folder, with slashes
	Size int64  `json:"size"`
}

// walkSource walks src as a build copies it: visit gets every folder and
// regular file below src, with its path relative to src, in the order
// filepath.WalkDir walks (lexical within each folder); the top-level target
// and .git folders are skipped, and a symlink (src itself included) or a
// device is an error naming it.
func walkSource(src string, visit func(rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && skippedFolders[rel]:
			return filepath.SkipDir
		case d.IsDir():
		case !d.Type().IsRegular():
			return fmt.Errorf("builder: %s is not a regular file (symlinks and devices are refused)", rel)
		}
		return visit(rel, d)
	})
}

// SourceFiles lists the regular files of dir that a build copies, in the
// order SourceDigests reads them.
func SourceFiles(dir string) ([]SourceFile, error) {
	var out []SourceFile
	err := walkSource(dir, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, SourceFile{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("builder: list source: %w", err)
	}
	return out, nil
}

// FileDigest is the sha256 of a file's bytes, in hex: what SourceDigests
// holds for it.
func FileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SourceDigests digests a module folder as CopySource would copy it: each
// regular file (skipping the top-level target and .git) by its relative
// path, with slashes, to the sha256 of its bytes, in hex. A symlink or a
// device is an error. The node compares it with the record of the folder as
// its own tools left it, file by file (R-RUN-17).
func SourceDigests(dir string) (map[string]string, error) {
	files, err := SourceFiles(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("builder: digest source: %w", err)
	}
	defer func() { _ = root.Close() }()
	out := make(map[string]string, len(files))
	for _, f := range files {
		d, err := hashFile(root, f)
		if err != nil {
			return nil, fmt.Errorf("builder: digest source: %s: %w", f.Path, err)
		}
		out[f.Path] = d
	}
	return out, nil
}

// hashFile is the digest of f's bytes. A file that is no longer a regular
// file of the size listed is an error: the folder changed under the walk.
func hashFile(root *os.Root, f SourceFile) (string, error) {
	in, err := root.Open(filepath.FromSlash(f.Path))
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return "", errors.New("changed while it was read")
	}
	h := sha256.New()
	n, err := io.CopyN(h, in, f.Size)
	if err != nil || n != f.Size {
		return "", errors.New("changed while it was read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
