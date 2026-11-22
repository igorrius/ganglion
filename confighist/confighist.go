// Package confighist is the history of the node's file, ganglion.yaml
// (R-CFG-13, ADR-0093). Every writer of the file goes through a History:
// it writes the file, copies it to config/ganglion.yaml in the workspace
// repository and commits the copy with a reason and a source. At start
// Sync commits a file that changed on the host. The live file stays the
// truth; the copy is only its record.
package confighist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/gitrepo"
	"go.opentelemetry.io/otel/metric"
)

// Source is who changed the file.
type Source string

// The sources of R-CFG-13.
const (
	SourcePropose     Source = "propose"
	SourceRollback    Source = "rollback"
	SourceModule      Source = "module"
	SourceDestination Source = "destination"
	SourceGroup       Source = "group"
	SourceHost        Source = "host"
	SourceMCP         Source = "mcp"
	SourceLink        Source = "link"
)

// CopyPath is where the copy of the file lives in the workspace.
const CopyPath = "config/ganglion.yaml"

// sourceLine starts the message line that names a change's source.
const sourceLine = "Source: "

// reasonHost is the reason of a change found at start.
const reasonHost = "the file changed on the host"

// Errors; callers decide on them with errors.Is.
var (
	// ErrNoHistory is a node that keeps no history (no git).
	ErrNoHistory = errors.New("confighist: this node keeps no history of its file (no git)")
	// ErrNoChange is an id that is no recorded change.
	ErrNoChange = errors.New("confighist: no such change")
)

// Repo is the workspace repository (gitrepo.Repo).
type Repo interface {
	Path(rel string) (string, error)
	Commit(ctx context.Context, paths []string, msg string) (string, error)
	Log(ctx context.Context, rel string, n int) ([]gitrepo.Entry, error)
	Diff(ctx context.Context, commit, rel string) (string, error)
	FileAt(ctx context.Context, commit, rel string, parent bool) ([]byte, bool, error)
}

// Change is one recorded change of the file.
type Change struct {
	ID     string
	When   time.Time
	Source Source
	Reason string
}

// History is the record of the node's file.
type History interface {
	// Record runs write under the history's lock, then copies the file and
	// commits it with reason and src. A failed write commits nothing.
	Record(ctx context.Context, src Source, reason string, write func() error) error
	// Sync commits a live file that differs from the last copy, with the
	// source host: an edit by hand, or a crash between a write and its
	// commit.
	Sync(ctx context.Context) error
	// Changes are the latest n changes, newest first.
	Changes(ctx context.Context, n int) ([]Change, error)
	// Diff is the change id as a unified diff of the file.
	Diff(ctx context.Context, id string) (string, error)
	// Versions are the file before and after the change id; before is
	// empty for the first record.
	Versions(ctx context.Context, id string) (before, after []byte, err error)
}

// Recorder is the History of a node with a workspace repository.
type Recorder struct {
	file string
	repo Repo
	log  *slog.Logger
	mu   sync.Mutex

	changes metric.Int64Counter
}

// New returns the history of the file at path, kept in repo.
func New(file string, repo Repo, log *slog.Logger) *Recorder {
	return &Recorder{file: file, repo: repo, log: log, changes: newChanges()}
}

// Record implements History.
func (r *Recorder) Record(ctx context.Context, src Source, reason string, write func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := write(); err != nil {
		return err
	}
	return r.commit(ctx, src, reason)
}

// Sync implements History.
func (r *Recorder) Sync(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	live, err := readOrEmpty(r.file)
	if err != nil {
		return err
	}
	at, err := r.repo.Path(CopyPath)
	if err != nil {
		return fmt.Errorf("confighist: sync: %w", err)
	}
	copied, err := readOrEmpty(at)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(at); statErr == nil && bytes.Equal(live, copied) {
		return nil
	}
	return r.commit(ctx, SourceHost, reasonHost)
}

// commit copies the live file and commits the copy. The caller holds mu.
func (r *Recorder) commit(ctx context.Context, src Source, reason string) error {
	live, err := readOrEmpty(r.file)
	if err != nil {
		return err
	}
	at, err := r.repo.Path(CopyPath)
	if err != nil {
		return fmt.Errorf("confighist: record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(at), 0o750); err != nil {
		return fmt.Errorf("confighist: record: %w", err)
	}
	if err := os.WriteFile(at, live, 0o600); err != nil {
		return fmt.Errorf("confighist: record: %w", err)
	}
	id, err := r.repo.Commit(ctx, []string{CopyPath}, message(src, reason))
	if err != nil {
		return fmt.Errorf("confighist: record: %w", err)
	}
	r.recorded(ctx, src)
	r.log.InfoContext(ctx, "config changed", "source", string(src), "reason", reason, "commit", id)
	return nil
}

// message is a commit message: the reason on one line, then the source.
func message(src Source, reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		reason = "no reason given"
	}
	return reason + "\n\n" + sourceLine + string(src)
}

// readOrEmpty reads path; a missing file is empty.
func readOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("confighist: %w", err)
	}
	return b, nil
}

// Changes implements History.
func (r *Recorder) Changes(ctx context.Context, n int) ([]Change, error) {
	es, err := r.repo.Log(ctx, CopyPath, n)
	if err != nil {
		return nil, fmt.Errorf("confighist: changes: %w", err)
	}
	cs := make([]Change, 0, len(es))
	for _, e := range es {
		cs = append(cs, changeOf(e))
	}
	return cs, nil
}

// changeOf reads a change from its commit.
func changeOf(e gitrepo.Entry) Change {
	c := Change{ID: e.ID, When: e.When}
	lines := strings.Split(e.Message, "\n")
	c.Reason = lines[0]
	for _, l := range lines[1:] {
		if s, ok := strings.CutPrefix(l, sourceLine); ok {
			c.Source = Source(strings.TrimSpace(s))
		}
	}
	return c
}

// Diff implements History.
func (r *Recorder) Diff(ctx context.Context, id string) (string, error) {
	if err := r.known(ctx, id); err != nil {
		return "", err
	}
	d, err := r.repo.Diff(ctx, id, CopyPath)
	if err != nil {
		return "", fmt.Errorf("confighist: diff: %w", err)
	}
	return d, nil
}

// Versions implements History.
func (r *Recorder) Versions(ctx context.Context, id string) (before, after []byte, err error) {
	if err := r.known(ctx, id); err != nil {
		return nil, nil, err
	}
	if before, _, err = r.repo.FileAt(ctx, id, CopyPath, true); err != nil {
		return nil, nil, fmt.Errorf("confighist: versions: %w", err)
	}
	if after, _, err = r.repo.FileAt(ctx, id, CopyPath, false); err != nil {
		return nil, nil, fmt.Errorf("confighist: versions: %w", err)
	}
	return before, after, nil
}

// historyDepth bounds how far back an id is looked up.
const historyDepth = 500

// known refuses an id that is not a recorded change of the file, so no
// other commit of the workspace is read through the history.
func (r *Recorder) known(ctx context.Context, id string) error {
	cs, err := r.Changes(ctx, historyDepth)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.ID == id || (len(id) >= 7 && strings.HasPrefix(c.ID, id)) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrNoChange, id)
}

// NoHistory is the History of a node without git: writes go through,
// nothing is recorded.
type NoHistory struct{}

// Record implements History: it only writes.
func (NoHistory) Record(_ context.Context, _ Source, _ string, write func() error) error {
	return write()
}

// Sync implements History.
func (NoHistory) Sync(context.Context) error { return nil }

// Changes implements History.
func (NoHistory) Changes(context.Context, int) ([]Change, error) { return nil, ErrNoHistory }

// Diff implements History.
func (NoHistory) Diff(context.Context, string) (string, error) { return "", ErrNoHistory }

// Versions implements History.
func (NoHistory) Versions(context.Context, string) ([]byte, []byte, error) {
	return nil, nil, ErrNoHistory
}
