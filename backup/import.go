package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// The place a restore leaves the export of the cells bucket for the node's
// first start (R-BAK-6): a folder of its own, since <data>/bus is the
// embedded server's store.
const (
	importDirName  = "bus-import"
	importFileName = "cells.jsonl"
)

// ImportDir is the folder in dataDir where a restore leaves the cells
// export.
func ImportDir(dataDir string) string { return filepath.Join(dataDir, importDirName) }

// ImportPath is the cells export a restore left in dataDir.
func ImportPath(dataDir string) string { return filepath.Join(ImportDir(dataDir), importFileName) }

// ImportFunc loads a cells export into the empty bucket and returns how many
// lines it applied. It fails, and leaves the bucket empty, for a bucket that
// holds entries.
type ImportFunc func(ctx context.Context, r io.Reader) (int, error)

// ImportPending loads the cells export a restore left in dataDir into the
// bucket through imp, then removes the file and its folder; it does nothing
// when there is none (R-BAK-6). A failed import keeps the file, so the next
// start tries again: the usual cause is a bucket that holds cells already,
// a restore made into a node that has run, which the operator must settle.
// It returns how many lines were applied.
func ImportPending(ctx context.Context, dataDir string, imp ImportFunc, log *slog.Logger) (int, error) {
	path := ImportPath(dataDir)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("backup: cells import: %w", err)
	}
	n, err := imp(ctx, f)
	_ = f.Close()
	if err != nil {
		return 0, fmt.Errorf("backup: cells import from %s (kept for the next start): %w", filepath.Join(importDirName, importFileName), err)
	}
	if err := os.Remove(path); err != nil {
		return n, fmt.Errorf("backup: cells import: %w", err)
	}
	// The folder is removed when nothing else lies in it.
	_ = os.Remove(ImportDir(dataDir))
	log.Info("backup: the cells of a restored backup were imported", "lines", n)
	return n, nil
}
