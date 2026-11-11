package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/gitrepo"
	"go.yaml.in/yaml/v3"
)

// Undo reverts commits, given oldest first, in one commit that names them
// (R-MEM-9): how the owner takes back one note's writes with one press. A
// second Undo of the same commits changes nothing. gitrepo.ErrConflict
// when a later write changed the same lines; nothing changes then.
func (m *Memory) Undo(ctx context.Context, commits []string) error {
	if len(commits) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.commitHandEdits(ctx, ""); err != nil {
		return err
	}
	var left []string
	for _, c := range commits {
		done, err := m.repo.Reverted(ctx, c)
		if err != nil {
			return fmt.Errorf("memory: undo: %w", err)
		}
		if !done {
			left = append(left, c)
		}
	}
	if len(left) == 0 {
		return nil
	}
	var msg strings.Builder
	msg.WriteString("memory: undo\n\n")
	for _, c := range left {
		msg.WriteString(gitrepo.UndoOf + c + "\n")
	}
	next, err := m.nextIDs()
	if err != nil {
		return err
	}
	_, err = m.repo.RevertAllFixed(ctx, left, msg.String(), func() ([]string, error) { return m.keepNextIDs(next) })
	m.observeUndo(ctx, err)
	if err != nil {
		return fmt.Errorf("memory: undo: %w", err)
	}
	return nil
}

// oldShapeKeys are frontmatter keys only a one-fact file has (ADR-0033).
var oldShapeKeys = []string{"checked", "source_turn"}

// Migrate moves the files in the one-fact shape of ADR-0033 into topics
// (ADR-0071): each becomes a topic with the same slug and title, no
// summary, and one fact f1 that keeps its text (whole, past the new
// limit), date, turn and taint. All move in one commit; nothing is
// committed when none is left. Quarantine is not touched.
func (m *Memory) Migrate(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	full, err := m.repo.Path(dirFacts)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	var paths []string
	for _, f := range files {
		slug, ok := strings.CutSuffix(f.Name(), ".md")
		if f.IsDir() || !ok || checkSlug(slug) != nil {
			continue
		}
		moved, err := m.move(slug)
		if err != nil {
			return err
		}
		if moved {
			paths = append(paths, pathOf(slug, CheckOK))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err := m.repo.Commit(ctx, paths, fmt.Sprintf("memory: move %d facts into topics (ADR-0071)\n", len(paths))); err != nil {
		return fmt.Errorf("memory: commit the move: %w", err)
	}
	return nil
}

// move rewrites the file slug as a topic when it is in the old shape.
func (m *Memory) move(slug string) (bool, error) {
	full, err := m.repo.Path(pathOf(slug, CheckOK))
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return false, fmt.Errorf("memory: %w", err)
	}
	if !oldShape(data) {
		return false, nil
	}
	e, err := decode(slug, CheckOK, data)
	if err != nil {
		m.log.Warn("memory: an old fact is unreadable and stays as it is", "slug", slug, "kind", errorKind(err))
		return false, nil
	}
	f := Fact{ID: "f1", Text: oneLine(e.Body), Turn: e.SourceTurn, Tainted: e.Tainted}
	if !e.Created.IsZero() {
		f.Date = day(e.Created)
	}
	out, err := encodeTopic(Topic{Slug: slug, Title: e.Title, Next: 2, Facts: []Fact{f}})
	if err != nil {
		return false, err
	}
	return true, m.writeFile(pathOf(slug, CheckOK), out)
}

// oldShape reports whether data has a frontmatter with a key only a
// one-fact file has.
func oldShape(data []byte) bool {
	rest, ok := bytes.CutPrefix(data, []byte(fence))
	if !ok {
		return false
	}
	head, _, ok := bytes.Cut(rest, []byte("\n"+fence))
	if !ok {
		return false
	}
	var keys map[string]any
	if yaml.Unmarshal(head, &keys) != nil {
		return false
	}
	return slices.ContainsFunc(oldShapeKeys, func(k string) bool { _, ok := keys[k]; return ok })
}

// nextIDs is every topic's id counter.
func (m *Memory) nextIDs() (map[string]int, error) {
	ts, err := m.Topics()
	if err != nil {
		return nil, err
	}
	next := make(map[string]int, len(ts))
	for _, t := range ts {
		next[t.Slug] = t.Next
	}
	return next, nil
}

// keepNextIDs sets each topic's id counter back to at least what it was
// before a revert, so an undone fact's id is never given again; it returns
// the files it rewrote.
func (m *Memory) keepNextIDs(before map[string]int) ([]string, error) {
	ts, err := m.Topics()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, t := range ts {
		if t.Next >= before[t.Slug] {
			continue
		}
		t.Next = before[t.Slug]
		data, err := encodeTopic(t)
		if err != nil {
			return nil, err
		}
		if err := m.writeFile(pathOf(t.Slug, CheckOK), data); err != nil {
			return nil, err
		}
		paths = append(paths, pathOf(t.Slug, CheckOK))
	}
	return paths, nil
}
