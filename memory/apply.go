package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
)

// OpKind is what an Op does.
type OpKind string

// Op kinds.
const (
	OpAdd     OpKind = "add"     // a fact to a topic; opens the topic when it is new
	OpReplace OpKind = "replace" // the fact Fact of Topic gets Text
	OpRemove  OpKind = "remove"  // the fact Fact of Topic
	OpDrop    OpKind = "drop"    // the whole topic
)

// MaxOps caps the operations of one Apply.
const MaxOps = 5

// changeKeyLine starts the commit message line that holds a change's key,
// so a rerun finds the commit it made.
const changeKeyLine = "Change-Key: "

// factID is a fact's id.
var factID = regexp.MustCompile(`^f[1-9][0-9]*$`)

// Op is one change. Topic is a slug or a title (any case); an add whose
// topic matches none opens one, and then Summary is required.
type Op struct {
	Kind    OpKind
	Topic   string
	Summary string
	Fact    string // the id, for replace and remove
	Text    string // for add and replace
}

// Source is where a change comes from (R-MEM-5).
type Source struct {
	Turn    string // the turn or the review's event, written beside each fact
	Key     string // the change's key: a rerun with the same key finds its commit
	Tainted bool   // the session held outside content
}

// Change is what one Op did.
type Change struct {
	Kind        OpKind
	Slug, Title string
	Fact        string // the id stored or removed
	Text        string
	Held        bool   // the check held it back: quarantined, not stored
	Reason      string // the check's reason, for the owner only (ADR-0034)
}

// Applied is the outcome of one Apply.
type Applied struct {
	Changes []Change
	Commit  string // "" when nothing was committed and no commit has the key
}

// Apply validates every op, checks each new text with the guard, and
// stores the result in one commit whose message holds the turn, the taint
// and the key. An op that repeats a fact stored by the same turn changes
// nothing, and a remove or drop whose target is gone is done when a commit
// already holds the key, so a rerun is safe; a file a crash left
// uncommitted is committed then (ruling R37). A held-back text goes to
// memory/quarantine/ and is not stored; a topic only it would open is not
// opened. ErrInvalid: a bad op, more than MaxOps, a full topic.
// ErrNotFound: a topic or fact that is not there. An error from the guard
// (the turn is ending) writes nothing.
func (m *Memory) Apply(ctx context.Context, ops []Op, src Source) (Applied, error) {
	if len(ops) == 0 {
		return Applied{}, nil
	}
	ops, err := checkOps(ops)
	if err != nil {
		return Applied{}, err
	}
	src.Key = oneLine(src.Key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.commitHandEdits(ctx, src.Turn); err != nil {
		return Applied{}, err
	}
	topics, err := m.Topics()
	if err != nil {
		return Applied{}, err
	}
	done := ""
	if src.Key != "" {
		if done, err = m.repo.Last(ctx, changeKeyLine+src.Key); err != nil {
			return Applied{}, fmt.Errorf("memory: %w", err)
		}
	}
	a := &applying{m: m, src: src, done: done, day: day(m.now()), topics: topics, changed: map[string]bool{}}
	for _, op := range ops {
		if err := a.apply(ctx, op); err != nil {
			return Applied{}, err
		}
	}
	commit, err := a.store(ctx)
	if err != nil {
		return Applied{}, err
	}
	m.observeChanges(ctx, a.changes, commit != done)
	return Applied{Changes: a.changes, Commit: commit}, nil
}

// checkOps validates the ops' shape and returns them with texts on one line.
func checkOps(ops []Op) ([]Op, error) {
	if len(ops) > MaxOps {
		return nil, fmt.Errorf("%w: at most %d changes at once", ErrInvalid, MaxOps)
	}
	out := make([]Op, len(ops))
	for i, op := range ops {
		op.Topic, op.Summary = oneLine(op.Topic), oneLine(op.Summary)
		if op.Topic == "" {
			return nil, fmt.Errorf("%w: a change needs a topic", ErrInvalid)
		}
		if utf8.RuneCountInString(op.Summary) > MaxSummaryRunes {
			return nil, fmt.Errorf("%w: a summary is longer than %d characters", ErrInvalid, MaxSummaryRunes)
		}
		switch op.Kind {
		case OpAdd, OpReplace:
			text, err := checkFactText(op.Text)
			if err != nil {
				return nil, err
			}
			op.Text = text
		case OpRemove, OpDrop:
		default:
			return nil, fmt.Errorf("%w: %q is not a kind of change", ErrInvalid, op.Kind)
		}
		if (op.Kind == OpReplace || op.Kind == OpRemove) && !factID.MatchString(op.Fact) {
			return nil, fmt.Errorf("%w: %q is not a fact's id (f1, f2…)", ErrInvalid, op.Fact)
		}
		out[i] = op
	}
	return out, nil
}

// applying is one Apply's work: the topics as they will be stored, which
// changed, and the quarantine files written.
type applying struct {
	m          *Memory
	src        Source
	done       string // the commit that already holds the key; "" if none
	day        time.Time
	topics     []Topic
	changed    map[string]bool // slug → written (or removed when dropped)
	dropped    []string
	touched    []string // every topic path an op named, for a leftover commit
	quarantine []string // quarantine files to commit
	held       []Entry  // held-back texts to write to quarantine
	changes    []Change
}

// apply does one op on the topics in memory.
func (a *applying) apply(ctx context.Context, op Op) error {
	i := a.find(op.Topic)
	if i < 0 && op.Kind == OpAdd {
		var err error
		if i, err = a.open(op); err != nil {
			return err
		}
	}
	if i < 0 {
		return a.gone(op, "")
	}
	a.touched = append(a.touched, pathOf(a.topics[i].Slug, CheckOK))
	switch op.Kind {
	case OpAdd:
		return a.add(ctx, i, op)
	case OpReplace:
		return a.replace(ctx, i, op)
	case OpRemove:
		return a.remove(i, op)
	case OpDrop:
		return a.drop(i)
	}
	return nil
}

// find is the index of the topic named by its slug, its title in any case,
// or another spelling of its slug ("Inverter!"); -1 when none.
func (a *applying) find(name string) int {
	if i := slices.IndexFunc(a.topics, func(t Topic) bool { return t.Slug == name }); i >= 0 {
		return i
	}
	if i := slices.IndexFunc(a.topics, func(t Topic) bool { return strings.EqualFold(t.Title, name) }); i >= 0 {
		return i
	}
	slug := Slug(name)
	return slices.IndexFunc(a.topics, func(t Topic) bool { return t.Slug == slug })
}

// open adds a new, empty topic titled by op.Topic; it is written only when
// a fact is stored in it.
func (a *applying) open(op Op) (int, error) {
	switch {
	case op.Summary == "":
		return -1, fmt.Errorf("%w: a new topic needs a one-line summary", ErrInvalid)
	case utf8.RuneCountInString(op.Topic) > MaxTitleRunes:
		return -1, fmt.Errorf("%w: a topic's title is longer than %d characters", ErrInvalid, MaxTitleRunes)
	}
	a.topics = append(a.topics, Topic{Slug: a.allocate(op.Topic), Title: op.Topic, Summary: op.Summary, Next: 1})
	return len(a.topics) - 1, nil
}

// allocate returns a free slug for title: its own, or with "-2", "-3"…. A
// slug whose file exists but does not parse is not free: the owner's file
// is never written over.
func (a *applying) allocate(title string) string {
	base := Slug(title)
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			slug = cut(base, MaxSlugLen-len(suffix)) + suffix
		}
		if !slices.ContainsFunc(a.topics, func(t Topic) bool { return t.Slug == slug }) && len(a.m.present([]string{pathOf(slug, CheckOK)})) == 0 {
			return slug
		}
	}
}

// gone is a target that is not there: done when a commit already holds the
// change's key (a rerun of a remove or drop), ErrNotFound otherwise.
func (a *applying) gone(op Op, slug string) error {
	if a.done != "" && (op.Kind == OpRemove || op.Kind == OpDrop) {
		a.changes = append(a.changes, Change{Kind: op.Kind, Slug: slug, Title: op.Topic, Fact: op.Fact})
		return nil
	}
	if slug == "" {
		return fmt.Errorf("%w: no topic %q", ErrNotFound, op.Topic)
	}
	return fmt.Errorf("%w: no fact %s in %s", ErrNotFound, op.Fact, slug)
}

// add stores op.Text as a new fact of topic i.
func (a *applying) add(ctx context.Context, i int, op Op) error {
	t := &a.topics[i]
	if j := slices.IndexFunc(t.Facts, func(f Fact) bool { return f.Text == op.Text && f.Turn == a.src.Turn && a.src.Turn != "" }); j >= 0 {
		a.changes = append(a.changes, a.change(op.Kind, *t, t.Facts[j]))
		return nil
	}
	if len(t.Facts) >= MaxTopicFacts {
		return fmt.Errorf("%w: the topic %s holds %d facts already", ErrInvalid, t.Slug, MaxTopicFacts)
	}
	held, err := a.check(ctx, *t, op)
	if err != nil || held {
		return err
	}
	f := Fact{ID: "f" + strconv.Itoa(t.Next), Text: op.Text, Date: a.day, Turn: a.src.Turn, Tainted: a.src.Tainted}
	t.Next++
	t.Facts = append(t.Facts, f)
	a.changed[t.Slug] = true
	a.changes = append(a.changes, a.change(op.Kind, *t, f))
	return nil
}

// replace gives fact op.Fact of topic i the text op.Text.
func (a *applying) replace(ctx context.Context, i int, op Op) error {
	t := &a.topics[i]
	j := slices.IndexFunc(t.Facts, func(f Fact) bool { return f.ID == op.Fact })
	if j < 0 {
		return a.gone(op, t.Slug)
	}
	if f := t.Facts[j]; f.Text == op.Text && f.Turn == a.src.Turn && a.src.Turn != "" {
		a.changes = append(a.changes, a.change(op.Kind, *t, f))
		return nil
	}
	held, err := a.check(ctx, *t, op)
	if err != nil || held {
		return err
	}
	t.Facts[j] = Fact{ID: op.Fact, Text: op.Text, Date: a.day, Turn: a.src.Turn, Tainted: a.src.Tainted}
	a.changed[t.Slug] = true
	a.changes = append(a.changes, a.change(op.Kind, *t, t.Facts[j]))
	return nil
}

// remove takes fact op.Fact out of topic i.
func (a *applying) remove(i int, op Op) error {
	t := &a.topics[i]
	j := slices.IndexFunc(t.Facts, func(f Fact) bool { return f.ID == op.Fact })
	if j < 0 {
		return a.gone(op, t.Slug)
	}
	f := t.Facts[j]
	t.Facts = slices.Delete(t.Facts, j, j+1)
	a.changed[t.Slug] = true
	a.changes = append(a.changes, a.change(op.Kind, *t, f))
	return nil
}

// drop removes topic i.
func (a *applying) drop(i int) error {
	t := a.topics[i]
	a.topics = slices.Delete(a.topics, i, i+1)
	a.dropped = append(a.dropped, t.Slug)
	delete(a.changed, t.Slug)
	a.changes = append(a.changes, Change{Kind: OpDrop, Slug: t.Slug, Title: t.Title})
	return nil
}

func (a *applying) change(k OpKind, t Topic, f Fact) Change {
	return Change{Kind: k, Slug: t.Slug, Title: t.Title, Fact: f.ID, Text: f.Text}
}

// check asks the guard about op's text with its topic's title (and the
// summary of a topic it opens). A held-back text is written to quarantine
// and recorded; a rerun finds it there and does not ask again.
func (a *applying) check(ctx context.Context, t Topic, op Op) (bool, error) {
	rel := quarantinePath(t.Slug, op.Text)
	if e, err := a.m.read(strings.TrimSuffix(strings.TrimPrefix(rel, dirQuarantine+"/"), ".md"), CheckQuarantined); err == nil && e.SourceTurn == a.src.Turn {
		a.quarantine = append(a.quarantine, rel)
		a.changes = append(a.changes, Change{Kind: op.Kind, Slug: t.Slug, Title: t.Title, Fact: op.Fact, Text: op.Text, Held: true, Reason: e.Reason})
		return true, nil
	}
	text := t.Title + "\n\n" + op.Text
	if len(t.Facts) == 0 && t.Summary != "" {
		text = t.Title + "\n" + t.Summary + "\n\n" + op.Text
	}
	v, err := a.m.guard.CheckFact(ctx, text)
	if err != nil {
		return false, fmt.Errorf("memory: check %s: %w", t.Slug, err)
	}
	if v.OK {
		return false, nil
	}
	e := Entry{Slug: strings.TrimSuffix(strings.TrimPrefix(rel, dirQuarantine+"/"), ".md"), Title: t.Title, Body: op.Text,
		Created: a.m.now().UTC(), SourceTurn: a.src.Turn, Tainted: a.src.Tainted, Checked: CheckQuarantined, Reason: v.Reason}
	a.held = append(a.held, e)
	a.quarantine = append(a.quarantine, rel)
	a.changes = append(a.changes, Change{Kind: op.Kind, Slug: t.Slug, Title: t.Title, Fact: op.Fact, Text: op.Text, Held: true, Reason: v.Reason})
	return true, nil
}

// quarantinePath is where a held-back text of the topic slug is kept: the
// slug and a short hash of the text, so the same text is one file.
func quarantinePath(slug, text string) string {
	return dirQuarantine + "/" + cut(slug, MaxSlugLen-9) + "-" + shortHash(text) + ".md"
}

// store writes the changed topics, removes the dropped ones and commits
// them with the quarantine files and every topic an op named. It returns
// the new commit, or the one that already holds the key when nothing was
// left to commit.
func (a *applying) store(ctx context.Context) (string, error) {
	for _, e := range a.held {
		data, err := encode(e)
		if err != nil {
			return "", err
		}
		if err := a.m.writeFile(pathOf(e.Slug, CheckQuarantined), data); err != nil {
			return "", err
		}
	}
	for _, t := range a.topics {
		if !a.changed[t.Slug] {
			continue
		}
		data, err := encodeTopic(withIDs(t))
		if err != nil {
			return "", err
		}
		if err := a.m.writeFile(pathOf(t.Slug, CheckOK), data); err != nil {
			return "", err
		}
	}
	paths := slices.Concat(a.m.present(a.touched), a.quarantine)
	for _, slug := range a.dropped {
		removed, err := a.m.remove(pathOf(slug, CheckOK))
		if err != nil {
			return "", err
		}
		paths = append(paths, removed...)
	}
	paths = slices.Compact(slices.Sorted(slices.Values(paths)))
	if len(paths) == 0 {
		return a.done, nil
	}
	before := a.m.head(ctx)
	after, err := a.m.repo.Commit(ctx, paths, a.message())
	if err != nil {
		return "", fmt.Errorf("memory: commit: %w", err)
	}
	if after == before {
		return a.done, nil
	}
	return after, nil
}

// message is the commit message: what changed, the turn, the taint, the key.
func (a *applying) message() string {
	var what []string
	for _, c := range a.changes {
		verb := string(c.Kind)
		if c.Held {
			verb = "quarantine"
		}
		what = append(what, strings.TrimSpace(verb+" "+c.Slug+" "+c.Fact))
	}
	msg := fmt.Sprintf("memory: %s\n\nTurn: %s\nTainted: %t\n", strings.Join(what, "; "), a.src.Turn, a.src.Tainted)
	if a.src.Key != "" {
		msg += changeKeyLine + a.src.Key + "\n"
	}
	return msg
}

// present keeps the paths that exist on disk: a topic an op named but
// never wrote has nothing to commit, and git refuses a path it never saw.
func (m *Memory) present(rels []string) []string {
	var out []string
	for _, rel := range rels {
		if full, err := m.repo.Path(rel); err == nil {
			if _, err := os.Stat(full); err == nil {
				out = append(out, rel)
			}
		}
	}
	return out
}

// head is the current commit, "" in a repository with none yet.
func (m *Memory) head(ctx context.Context) string {
	h, err := m.repo.Head(ctx)
	if err != nil {
		return ""
	}
	return h
}

// day is t's date at midnight UTC: a fact records its day, not its time.
func day(t time.Time) time.Time {
	y, mo, d := t.UTC().Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
}

// Topics returns every topic in slug order. An unreadable file is logged
// and skipped.
func (m *Memory) Topics() ([]Topic, error) {
	full, err := m.repo.Path(dirFacts)
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
	var out []Topic
	for _, f := range files {
		slug, ok := strings.CutSuffix(f.Name(), ".md")
		if f.IsDir() || !ok || checkSlug(slug) != nil {
			continue
		}
		t, err := m.topic(slug)
		if err != nil {
			m.log.Warn("memory: topic unreadable", "slug", slug, "kind", errorKind(err))
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// Topic returns the topic slug; ErrNotFound when there is none.
func (m *Memory) Topic(slug string) (Topic, error) {
	if err := checkSlug(slug); err != nil {
		return Topic{}, err
	}
	return m.topic(slug)
}

func (m *Memory) topic(slug string) (Topic, error) {
	full, err := m.repo.Path(pathOf(slug, CheckOK))
	if err != nil {
		return Topic{}, err
	}
	data, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return Topic{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err != nil {
		return Topic{}, fmt.Errorf("memory: %w", err)
	}
	return decodeTopic(slug, data)
}

// Notice returns the alerts about c: one to every admin when it was
// held back, none otherwise. The key holds the topic and a short hash of
// the text, so the same held-back text (a rerun) alerts once.
func (m *Memory) Notice(ctx context.Context, c Change) ([]event.Outgoing, error) {
	if !c.Held {
		return nil, nil
	}
	text := fmt.Sprintf("A fact was held back as a possible injection: %s — %s.", c.Title, strings.TrimRight(c.Reason, ". "))
	out, err := m.alerts.Messages(ctx, "alert:memory:quarantine:"+c.Slug+":"+shortHash(c.Text), text)
	if err != nil {
		return nil, fmt.Errorf("memory: quarantine alert: %w", err)
	}
	return out, nil
}

// commitHandEdits commits the owner's uncommitted changes to topic files in
// a commit of their own, so a change's commit, and its Undo, hold only that
// change (R-MEM-9). A file that already holds a fact of turn is this
// change's own, left by a crash between its write and its commit: the
// change commits it.
func (m *Memory) commitHandEdits(ctx context.Context, turn string) error {
	changed, err := m.repo.Changed(ctx, dirFacts)
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	var paths []string
	for _, rel := range changed {
		slug, ok := strings.CutSuffix(strings.TrimPrefix(rel, dirFacts+"/"), ".md")
		if !ok || strings.Contains(slug, "/") {
			continue // quarantine and anything else is not a topic
		}
		if t, err := m.topic(slug); err == nil && turn != "" && slices.ContainsFunc(t.Facts, func(f Fact) bool { return f.Turn == turn }) {
			continue
		}
		paths = append(paths, rel)
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err := m.repo.Commit(ctx, paths, "memory: hand edits\n"); err != nil {
		return fmt.Errorf("memory: commit hand edits: %w", err)
	}
	return nil
}

// withIDs is t with an id for every line the owner wrote by hand: it gets
// one at the next write to its topic, so it can be corrected or forgotten.
func withIDs(t Topic) Topic {
	t.Facts = slices.Clone(t.Facts)
	for i := range t.Facts {
		if t.Facts[i].ID == "" {
			t.Facts[i].ID, t.Next = "f"+strconv.Itoa(t.Next), t.Next+1
		}
	}
	return t
}
