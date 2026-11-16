package gitrepo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Entry is one commit of a file's history.
type Entry struct {
	// ID is the full commit id.
	ID string
	// When is the commit's time.
	When time.Time
	// Message is the whole message: the subject, a blank line, the body.
	Message string
}

// Log returns at most n commits that changed rel, newest first. A
// repository with no commit has none.
func (r *Repo) Log(ctx context.Context, rel string, n int) ([]Entry, error) {
	if err := checkPath(rel); err != nil {
		return nil, err
	}
	if n < 1 {
		return nil, fmt.Errorf("gitrepo: log: %d entries", n)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return nil, nil // no commit yet
	}
	out, err := r.run(ctx, "log", "-n", strconv.Itoa(n), "--format=%H%x00%ct%x00%B%x1e", "--", rel)
	if err != nil {
		return nil, err
	}
	var es []Entry
	for rec := range strings.SplitSeq(out, "\x1e") {
		e, ok, err := entryOf(rec)
		if err != nil {
			return nil, err
		}
		if ok {
			es = append(es, e)
		}
	}
	return es, nil
}

// entryOf parses one record of Log's format; false for the empty tail.
func entryOf(rec string) (Entry, bool, error) {
	rec = strings.TrimLeft(rec, "\n")
	if rec == "" {
		return Entry{}, false, nil
	}
	parts := strings.SplitN(rec, "\x00", 3)
	if len(parts) != 3 {
		return Entry{}, false, fmt.Errorf("gitrepo: log: a record of %d fields", len(parts))
	}
	sec, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Entry{}, false, fmt.Errorf("gitrepo: log: time %q: %w", parts[1], err)
	}
	return Entry{ID: parts[0], When: time.Unix(sec, 0), Message: strings.TrimSpace(parts[2])}, true, nil
}

// Diff returns the unified diff of rel in commit against its parent; for
// a root commit the whole file shows as added.
func (r *Repo) Diff(ctx context.Context, commit, rel string) (string, error) {
	if err := checkCommit(commit, rel); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run(ctx, "show", "--format=", "--no-color", "--no-ext-diff", commit, "--", rel)
}

// FileAt returns rel as it was at commit, or at its parent when parent is
// set; false when the file did not exist there (a root commit has no
// parent, so nothing existed before it).
func (r *Repo) FileAt(ctx context.Context, commit, rel string, parent bool) ([]byte, bool, error) {
	if err := checkCommit(commit, rel); err != nil {
		return nil, false, err
	}
	rev := commit
	if parent {
		rev += "^"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.run(ctx, "cat-file", "-e", rev+":"+rel); err != nil {
		if _, err := r.run(ctx, "cat-file", "-e", commit+"^{commit}"); err != nil {
			return nil, false, err // commit itself is not there
		}
		return nil, false, nil
	}
	out, err := r.run(ctx, "show", "--no-textconv", rev+":"+rel)
	if err != nil {
		return nil, false, err
	}
	return []byte(out), true, nil
}

// checkCommit refuses a commit id or a path that must not reach git.
func checkCommit(commit, rel string) error {
	if !commitPattern.MatchString(commit) {
		return fmt.Errorf("gitrepo: %q is not a commit id", commit)
	}
	return checkPath(rel)
}
