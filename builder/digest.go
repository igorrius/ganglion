package builder

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
// order SourceDigest hashes them.
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

// SourceDigest hashes a module folder as CopySource would copy it: regular
// files in path order (skipping the top-level target and .git), each as its
// relative path, a zero byte, its length and its bytes, in sha256, in hex.
// A symlink or a device is an error. The node compares it with the record
// of the folder as its own tools left it (R-RUN-17).
func SourceDigest(dir string) (string, error) {
	files, err := SourceFiles(dir)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("builder: digest source: %w", err)
	}
	defer func() { _ = root.Close() }()
	h := sha256.New()
	for _, f := range files {
		if err := hashFile(h, root, f); err != nil {
			return "", fmt.Errorf("builder: digest source: %s: %w", f.Path, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile writes f's framing and bytes into h. A file that is no longer a
// regular file of the size listed is an error: the folder changed under the
// walk.
func hashFile(h io.Writer, root *os.Root, f SourceFile) error {
	in, err := root.Open(filepath.FromSlash(f.Path))
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return fmt.Errorf("changed while it was read")
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(f.Size)) //nolint:gosec // a file's size is not negative
	if _, err := io.WriteString(h, f.Path); err != nil {
		return err
	}
	if _, err := h.Write(append([]byte{0}, size[:]...)); err != nil {
		return err
	}
	n, err := io.CopyN(h, in, f.Size)
	if err != nil || n != f.Size {
		return fmt.Errorf("changed while it was read")
	}
	return nil
}
