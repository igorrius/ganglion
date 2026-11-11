// Package memory keeps what the agent knows about its owner as Markdown
// topics in the workspace's git repository: one file per topic, one line
// per fact (R-MEM-3, R-MEM-5, R-MEM-6; ADR-0033, ADR-0071). Every new text
// passes the clean-context check (R-POL-7, ADR-0034): one that is not a
// plain fact goes to memory/quarantine/, is never shown to the model and
// the admins are told. Every change is a commit, so a bad one is undone.
// Every topic reaches the model whole in the system prompt within a
// budget (R-MEM-7); recall also searches archived sessions.
package memory

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

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/guard"
	"go.yaml.in/yaml/v3"
)

// Errors; callers decide on them with errors.Is.
var (
	// ErrNotFound is a slug with no stored fact.
	ErrNotFound = errors.New("memory: no such fact")
	// ErrInvalid is an entry, a slug or a query that cannot be used.
	ErrInvalid = errors.New("memory: invalid")
)

// errFrontmatter is an entry whose frontmatter does not parse.
var errFrontmatter = errors.New("the frontmatter is not readable")

// errorKind names what made an entry unreadable, for the log: never the
// error's text, which could quote the entry.
func errorKind(err error) string {
	if errors.Is(err, errFrontmatter) {
		return "frontmatter"
	}
	return "io"
}

// Check is the outcome of the clean-context check of an entry. It is
// stored in the entry's file, so its values never change.
type Check string

// Checks.
const (
	CheckOK          Check = "ok"
	CheckQuarantined Check = "quarantined"
)

// MaxTitleRunes caps a topic's title.
const MaxTitleRunes = 100

// Directories of the repository.
const (
	dirFacts      = "memory"
	dirQuarantine = "memory/quarantine"
)

// Entry is a held-back text in memory/quarantine/, in the one-fact shape
// of ADR-0033 (ruling 10 of the M5 plan). Created, SourceTurn and Tainted
// say where it came from (R-MEM-5); Checked and Reason are the guard's
// verdict.
type Entry struct {
	Slug       string
	Title      string
	Body       string
	Created    time.Time
	SourceTurn string
	Tainted    bool
	Checked    Check
	Reason     string
}

// Guard is the clean-context check (guard.Guard).
type Guard interface {
	CheckFact(ctx context.Context, fact string) (guard.Verdict, error)
	CheckPersona(ctx context.Context, persona string) (guard.Verdict, error)
}

// Option configures a Memory.
type Option func(*Memory)

// WithAlerts sets who hears about a quarantined entry. The default tells
// nobody.
func WithAlerts(a alert.Admins) Option { return func(m *Memory) { m.alerts = a } }

// WithEpisodes sets the archive recall searches besides the facts. The
// default has none.
func WithEpisodes(e Episodes) Option { return func(m *Memory) { m.episodes = e } }

// WithLogger sets where unreadable entries are reported. The default
// drops it.
func WithLogger(l *slog.Logger) Option { return func(m *Memory) { m.log = l } }

// Memory is the facts in the workspace repository. Writes and forgets
// are made one at a time.
type Memory struct {
	repo     *gitrepo.Repo
	guard    Guard
	now      func() time.Time
	alerts   alert.Admins
	episodes Episodes
	log      *slog.Logger
	in       instruments
	mu       sync.Mutex
}

// New returns the memory in repo, checked by g.
func New(repo *gitrepo.Repo, g Guard, now func() time.Time, opts ...Option) *Memory {
	m := &Memory{
		repo: repo, guard: g, now: now, episodes: NoEpisodes{}, log: slog.New(slog.DiscardHandler), in: newInstruments(),
		alerts: alert.Admins{List: func(context.Context) ([]string, error) { return nil, nil }},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// oneLine is s on one line: every run of white space, line breaks
// included, becomes one space, so a title cannot add lines to the index.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// writeFile writes data to rel in the repository, creating its
// directory. It writes a temporary file and renames it, so a reader (the
// index of a turn running at the same time) never sees half a file.
func (m *Memory) writeFile(rel string, data []byte) error {
	full, err := m.repo.Path(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".write-*")
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("memory: %w", err)
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("memory: %w", err)
	}
	return nil
}

// remove deletes rel when it exists and returns it for the commit.
func (m *Memory) remove(rel string) ([]string, error) {
	full, err := m.repo.Path(rel)
	if err != nil {
		return nil, err
	}
	switch err := os.Remove(full); {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("memory: %w", err)
	}
	return []string{rel}, nil
}

// read returns the entry slug from the directory of check.
func (m *Memory) read(slug string, check Check) (Entry, error) {
	full, err := m.repo.Path(pathOf(slug, check))
	if err != nil {
		return Entry{}, err
	}
	data, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("memory: %w", err)
	}
	return decode(slug, check, data)
}

// pathOf is where the entry slug with check lives in the repository.
func pathOf(slug string, check Check) string {
	if check == CheckQuarantined {
		return dirQuarantine + "/" + slug + ".md"
	}
	return dirFacts + "/" + slug + ".md"
}

// front is an entry's frontmatter.
type front struct {
	Title      string    `yaml:"title"`
	Created    time.Time `yaml:"created"`
	SourceTurn string    `yaml:"source_turn"`
	Tainted    bool      `yaml:"tainted"`
	Checked    Check     `yaml:"checked"`
	Reason     string    `yaml:"reason,omitempty"`
}

const fence = "---\n"

// encode is the file of e: YAML frontmatter, then the body.
func encode(e Entry) ([]byte, error) {
	y, err := yaml.Marshal(front{Title: e.Title, Created: e.Created, SourceTurn: e.SourceTurn, Tainted: e.Tainted,
		Checked: e.Checked, Reason: e.Reason})
	if err != nil {
		return nil, fmt.Errorf("memory: encode %s: %w", e.Slug, err)
	}
	return []byte(fence + string(y) + fence + "\n" + e.Body + "\n"), nil
}

// decode reads the file of the entry slug from the directory of check.
// A file without frontmatter (written by the owner by hand) is a fact
// titled by its slug. The directory decides whether the entry is
// quarantined, whatever its frontmatter says.
func decode(slug string, check Check, data []byte) (Entry, error) {
	e := Entry{Slug: slug, Title: slug, Checked: check}
	rest, ok := bytes.CutPrefix(data, []byte(fence))
	if !ok {
		e.Body = strings.TrimSpace(string(data))
		return e, nil
	}
	head, body, ok := bytes.Cut(rest, []byte("\n"+fence))
	if !ok {
		return Entry{}, fmt.Errorf("memory: %s: %w: it does not end", slug, errFrontmatter)
	}
	var f front
	if err := yaml.Unmarshal(head, &f); err != nil {
		// The parser's message can quote the file's values: it stays out.
		return Entry{}, fmt.Errorf("memory: %s: %w", slug, errFrontmatter)
	}
	if t := oneLine(f.Title); t != "" {
		e.Title = t
	}
	e.Body, e.Created, e.SourceTurn, e.Tainted, e.Reason = strings.TrimSpace(string(body)), f.Created, f.SourceTurn, f.Tainted, f.Reason
	return e, nil
}
