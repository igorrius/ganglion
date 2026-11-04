// Package logfile writes the node's logs to disk (R-OBS-6, ADR-0043): one
// file that grows to a size limit and is then renamed aside, with the
// renamed files pruned by count and age so the disk never fills. Writes
// are synchronous, with no buffer and no goroutine: a kill -9 loses
// nothing the kernel has received.
package logfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// CurrentName is the file being written; a rotated file is named
// ganglion-<UTC yyyymmddThhmmss.mmmZ>.log.
const CurrentName = "ganglion.log"

// stampLayout is the time in a rotated file's name.
const stampLayout = "20060102T150405.000Z"

// rotated matches the name of a rotated file; nothing else is pruned.
var rotated = regexp.MustCompile(`^ganglion-(\d{8}T\d{6}\.\d{3}Z)(?:-\d+)?\.log$`)

// Config sets where the logs go and how much of them is kept.
type Config struct {
	Dir      string
	MaxBytes int64         // a file is rotated before a write would pass this
	MaxFiles int           // rotated files kept; 0 keeps none
	MaxAge   time.Duration // rotated files older than this are removed
}

// DefaultConfig keeps 10 MiB a file, 5 rotated files and 14 days.
func DefaultConfig(dir string) Config {
	return Config{Dir: dir, MaxBytes: 10 << 20, MaxFiles: 5, MaxAge: 14 * 24 * time.Hour}
}

// Rotator is an io.Writer over the current log file. It is safe for
// concurrent use; each Write is one write to the file.
type Rotator struct {
	cfg Config
	now func() time.Time

	mu   sync.Mutex
	f    *os.File
	size int64

	rotations metric.Int64Counter
	errs      metric.Int64Counter
}

// Open creates the directory (0700) and opens the current file (0600)
// for appending, then prunes old rotated files.
func Open(cfg Config, now func() time.Time) (*Rotator, error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("logfile: %w", err)
	}
	m := otel.Meter("ganglion/logfile")
	r := &Rotator{cfg: cfg, now: now}
	r.rotations, _ = m.Int64Counter("ganglion.log.rotations", metric.WithDescription("Log files rotated on disk."))
	r.errs, _ = m.Int64Counter("ganglion.log.write_errors", metric.WithDescription("Log lines that could not be written to disk."))
	if err := r.openCurrent(); err != nil {
		return nil, err
	}
	r.prune()
	return r, nil
}

func (r *Rotator) openCurrent() error {
	f, err := os.OpenFile(filepath.Join(r.cfg.Dir, CurrentName), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logfile: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("logfile: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write writes p to the current file, rotating it first when p would
// take it past MaxBytes. A failed write is counted and returned.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		r.errs.Add(context.Background(), 1)
		return 0, errors.New("logfile: closed")
	}
	if r.size > 0 && r.size+int64(len(p)) > r.cfg.MaxBytes {
		if err := r.rotate(); err != nil {
			r.errs.Add(context.Background(), 1)
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	if err != nil {
		r.errs.Add(context.Background(), 1)
		return n, fmt.Errorf("logfile: %w", err)
	}
	return n, nil
}

// rotate renames the current file aside, opens a new one and prunes.
func (r *Rotator) rotate() error {
	if err := r.f.Close(); err != nil {
		return fmt.Errorf("logfile: %w", err)
	}
	r.f = nil
	stamp := r.now().UTC().Format(stampLayout)
	name := filepath.Join(r.cfg.Dir, "ganglion-"+stamp+".log")
	for i := 1; exists(name); i++ { // two rotations in one millisecond
		name = filepath.Join(r.cfg.Dir, fmt.Sprintf("ganglion-%s-%d.log", stamp, i))
	}
	if err := os.Rename(filepath.Join(r.cfg.Dir, CurrentName), name); err != nil {
		_ = r.openCurrent() // keep writing to the same file rather than nowhere
		return fmt.Errorf("logfile: %w", err)
	}
	if err := r.openCurrent(); err != nil {
		return err
	}
	r.rotations.Add(context.Background(), 1)
	r.prune()
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// prune removes rotated files past MaxFiles, newest kept, and those
// older than MaxAge by the time in their name.
func (r *Rotator) prune() {
	es, err := os.ReadDir(r.cfg.Dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		at   time.Time
	}
	var files []file
	for _, e := range es {
		m := rotated.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		at, err := time.Parse(stampLayout, m[1])
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), at})
	}
	slices.SortFunc(files, func(a, b file) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return len(b.name) - len(a.name) // ganglion-<t>-1.log is newer than ganglion-<t>.log
	})
	cutoff := r.now().Add(-r.cfg.MaxAge)
	for i, f := range files {
		if i >= r.cfg.MaxFiles || f.at.Before(cutoff) {
			_ = os.Remove(filepath.Join(r.cfg.Dir, f.name))
		}
	}
}

// Close closes the current file; later writes fail.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
