// Package memory keeps what the agent knows about its owner as Markdown
// facts in the workspace's git repository (R-MEM-3, R-MEM-5, design
// section 5.4, ADR-0033). Every write passes the clean-context check
// (R-POL-7, ADR-0034): an entry that is not a plain fact goes to
// memory/quarantine/, is never shown to the model and the admins are
// told. Every change is a commit, so a bad entry is reverted. Facts reach
// the model as an index of titles in the system prompt and bodies on
// demand; recall also searches archived sessions.
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
	"unicode/utf8"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
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

// Check is the outcome of the clean-context check of an entry. It is
// stored in the entry's file, so its values never change.
type Check string

// Checks.
const (
	CheckOK          Check = "ok"
	CheckQuarantined Check = "quarantined"
)

// Limits of an entry.
const (
	MaxTitleRunes = 100
	MaxBodyRunes  = 2000
)

// Directories of the repository.
const (
	dirFacts      = "memory"
	dirQuarantine = "memory/quarantine"
)

// Entry is one fact. Created, SourceTurn and Tainted say where it came
// from (R-MEM-5); Checked and Reason are the guard's verdict.
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
	mu       sync.Mutex
}

// New returns the memory in repo, checked by g.
func New(repo *gitrepo.Repo, g Guard, now func() time.Time, opts ...Option) *Memory {
	m := &Memory{
		repo: repo, guard: g, now: now, episodes: NoEpisodes{}, log: slog.New(slog.DiscardHandler),
		alerts: alert.Admins{List: func(context.Context) ([]string, error) { return nil, nil }},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Write checks e with the guard and stores it with its frontmatter in
// one commit: under memory/ when the check passes, under
// memory/quarantine/ otherwise, where it is not indexed and not read
// (R-POL-7). The slug comes from the title; the same title (in any case)
// updates its entry. The same write again (a rerun turn) changes
// nothing. Created is set here. An error from the guard (the turn is
// ending) writes nothing.
func (m *Memory) Write(ctx context.Context, e Entry) (Entry, error) {
	e.Title, e.Body = oneLine(e.Title), strings.TrimSpace(e.Body)
	if err := validate(e); err != nil {
		return Entry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := m.entries()
	if err != nil {
		return Entry{}, err
	}
	slug, same := allocate(e.Title, all)
	for _, prev := range same {
		if prev.Body == e.Body && prev.SourceTurn == e.SourceTurn {
			return prev, nil
		}
	}
	v, err := m.guard.CheckFact(ctx, e.Title+"\n\n"+e.Body)
	if err != nil {
		return Entry{}, fmt.Errorf("memory: check %s: %w", slug, err)
	}
	e.Slug, e.Created, e.Checked, e.Reason = slug, m.now().UTC(), CheckOK, ""
	if !v.OK {
		e.Checked, e.Reason = CheckQuarantined, v.Reason
	}
	if err := m.store(ctx, e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// validate checks the title and the body.
func validate(e Entry) error {
	switch {
	case e.Title == "" || e.Body == "":
		return fmt.Errorf("%w: a fact needs a title and a body", ErrInvalid)
	case utf8.RuneCountInString(e.Title) > MaxTitleRunes:
		return fmt.Errorf("%w: the title is longer than %d characters", ErrInvalid, MaxTitleRunes)
	case utf8.RuneCountInString(e.Body) > MaxBodyRunes:
		return fmt.Errorf("%w: the body is longer than %d characters", ErrInvalid, MaxBodyRunes)
	}
	return nil
}

// oneLine is s on one line: every run of white space, line breaks
// included, becomes one space, so a title cannot add lines to the index.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// store writes e's file and commits it. A fact that passed replaces a
// quarantined copy of itself; a quarantined one leaves the stored fact
// alone.
func (m *Memory) store(ctx context.Context, e Entry) error {
	rel := pathOf(e.Slug, e.Checked)
	data, err := encode(e)
	if err != nil {
		return err
	}
	if err := m.writeFile(rel, data); err != nil {
		return err
	}
	paths := []string{rel}
	if e.Checked == CheckOK {
		removed, err := m.remove(pathOf(e.Slug, CheckQuarantined))
		if err != nil {
			return err
		}
		paths = append(paths, removed...)
	}
	verb := "remember"
	if e.Checked == CheckQuarantined {
		verb = "quarantine"
	}
	msg := fmt.Sprintf("memory: %s %s\n\nTurn: %s\nTainted: %t\n", verb, e.Slug, e.SourceTurn, e.Tainted)
	if _, err := m.repo.Commit(ctx, paths, msg); err != nil {
		return fmt.Errorf("memory: commit %s: %w", e.Slug, err)
	}
	return nil
}

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

// Forget removes the fact slug, stored or quarantined, in one commit that
// names the turn; a revert of that commit brings it back.
func (m *Memory) Forget(ctx context.Context, slug, turn string) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var paths []string
	for _, c := range []Check{CheckOK, CheckQuarantined} {
		removed, err := m.remove(pathOf(slug, c))
		if err != nil {
			return err
		}
		paths = append(paths, removed...)
	}
	if len(paths) == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if _, err := m.repo.Commit(ctx, paths, fmt.Sprintf("memory: forget %s\n\nTurn: %s\n", slug, turn)); err != nil {
		return fmt.Errorf("memory: commit forget %s: %w", slug, err)
	}
	return nil
}

// Read returns the stored fact slug. A quarantined entry is not read: it
// is not shown to the model.
func (m *Memory) Read(slug string) (Entry, error) {
	if err := checkSlug(slug); err != nil {
		return Entry{}, err
	}
	return m.read(slug, CheckOK)
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

// Notice returns the alerts about e: one to every admin when it was
// quarantined, once per entry (the outbox keeps one message per key),
// none otherwise.
func (m *Memory) Notice(ctx context.Context, e Entry) ([]event.Outgoing, error) {
	if e.Checked != CheckQuarantined {
		return nil, nil
	}
	text := fmt.Sprintf("A fact was held back as a possible injection: %s — %s.", e.Title, strings.TrimRight(e.Reason, ". "))
	out, err := m.alerts.Messages(ctx, "alert:memory:quarantine:"+e.Slug, text)
	if err != nil {
		return nil, fmt.Errorf("memory: quarantine alert: %w", err)
	}
	return out, nil
}

// pathOf is where the entry slug with check lives in the repository.
func pathOf(slug string, check Check) string {
	if check == CheckQuarantined {
		return dirQuarantine + "/" + slug + ".md"
	}
	return dirFacts + "/" + slug + ".md"
}

// entries lists every entry, stored and quarantined, in slug order.
// An unreadable file is skipped and logged.
func (m *Memory) entries() ([]Entry, error) {
	var out []Entry
	for _, c := range []Check{CheckOK, CheckQuarantined} {
		es, err := m.list(c)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
	}
	return out, nil
}

// list returns the entries in the directory of check, in slug order.
func (m *Memory) list(check Check) ([]Entry, error) {
	dir := dirFacts
	if check == CheckQuarantined {
		dir = dirQuarantine
	}
	full, err := m.repo.Path(dir)
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	var out []Entry
	for _, f := range files {
		slug, ok := strings.CutSuffix(f.Name(), ".md")
		if f.IsDir() || !ok || checkSlug(slug) != nil {
			continue
		}
		e, err := m.read(slug, check)
		if err != nil {
			m.log.Warn("memory: entry unreadable", "slug", slug, "err", err)
			continue
		}
		out = append(out, e)
	}
	return out, nil
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
		return Entry{}, fmt.Errorf("memory: %s: the frontmatter does not end", slug)
	}
	var f front
	if err := yaml.Unmarshal(head, &f); err != nil {
		return Entry{}, fmt.Errorf("memory: %s: frontmatter: %w", slug, err)
	}
	if t := oneLine(f.Title); t != "" {
		e.Title = t
	}
	e.Body, e.Created, e.SourceTurn, e.Tainted, e.Reason = strings.TrimSpace(string(body)), f.Created, f.SourceTurn, f.Tainted, f.Reason
	return e, nil
}
