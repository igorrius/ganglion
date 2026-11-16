package gitrepo_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/gitrepo"
)

// needGit skips a test on a host without the git binary.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the git binary is not installed; the workspace repository needs it (ADR-0033)")
	}
}

func open(t *testing.T) *gitrepo.Repo {
	t.Helper()
	needGit(t)
	r, err := gitrepo.Open(context.Background(), filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func write(t *testing.T, r *gitrepo.Repo, path, text string) {
	t.Helper()
	full := filepath.Join(r.Dir(), path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestOpenInitsOnceAndCommits(t *testing.T) {
	r := open(t)
	if _, err := os.Stat(filepath.Join(r.Dir(), ".git")); err != nil {
		t.Fatalf("no repository: %v", err)
	}
	write(t, r, "memory/a.md", "one")
	hash, err := r.Commit(context.Background(), []string{"memory/a.md"}, "memory: remember a")
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, r.Dir(), "log", "-1", "--format=%H %an <%ae> %s"); got != hash+" ganglion <ganglion@localhost> memory: remember a" {
		t.Fatalf("last commit %q, hash %s", got, hash)
	}
	// A second Open keeps the history.
	again, err := gitrepo.Open(context.Background(), r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, again.Dir(), "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("%s commits after reopening", got)
	}
}

func TestCommitWithoutChangesKeepsHead(t *testing.T) {
	r := open(t)
	write(t, r, "memory/a.md", "one")
	first, err := r.Commit(context.Background(), []string{"memory/a.md"}, "one")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Commit(context.Background(), []string{"memory/a.md"}, "one again")
	if err != nil || again != first {
		t.Fatalf("commit without changes: %s, %v; want %s", again, err, first)
	}
}

func TestCommitOnlyTheGivenPaths(t *testing.T) {
	r := open(t)
	write(t, r, "memory/a.md", "one")
	write(t, r, "persona/10-me.md", "the owner's draft")
	if _, err := r.Commit(context.Background(), []string{"memory/a.md"}, "a"); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, r.Dir(), "ls-files"); got != "memory/a.md" {
		t.Fatalf("tracked %q", got)
	}
}

func TestRemovedFileIsCommittedAndRevertable(t *testing.T) {
	r := open(t)
	ctx := context.Background()
	write(t, r, "memory/a.md", "one")
	if _, err := r.Commit(ctx, []string{"memory/a.md"}, "a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.Dir(), "memory/a.md")); err != nil {
		t.Fatal(err)
	}
	forget, err := r.Commit(ctx, []string{"memory/a.md"}, "forget a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Revert(ctx, forget); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(r.Dir(), "memory/a.md")); err != nil || string(b) != "one" {
		t.Fatalf("after revert: %q, %v", b, err)
	}
}

func TestPathsOutsideAreRefused(t *testing.T) {
	r := open(t)
	for _, p := range []string{"../x.md", "/etc/passwd", "memory/../../x", "", ".git/config"} {
		if _, err := r.Commit(context.Background(), []string{p}, "x"); !errors.Is(err, gitrepo.ErrOutside) {
			t.Errorf("%q: %v, want ErrOutside", p, err)
		}
	}
}

func TestRevertRefusesAnOption(t *testing.T) {
	r := open(t)
	if _, err := r.Revert(context.Background(), "--abort"); err == nil {
		t.Fatal("an option was taken for a commit")
	}
}

func TestOpenWithoutGitIsAClearError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := gitrepo.Open(context.Background(), t.TempDir())
	if !errors.Is(err, gitrepo.ErrNoGit) {
		t.Fatalf("%v, want ErrNoGit", err)
	}
}

func TestOpenRefusesAnEmptyDir(t *testing.T) {
	needGit(t)
	if _, err := gitrepo.Open(context.Background(), ""); err == nil {
		t.Fatal("an empty directory was opened")
	}
}

// The environment of a git hook (GIT_DIR and others) must not send the
// commands to another repository.
func TestGitEnvironmentIsIgnored(t *testing.T) {
	needGit(t)
	other := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	r, err := gitrepo.Open(context.Background(), filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, ".git")); err == nil {
		t.Fatal("git init went to GIT_DIR")
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), ".git")); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkCommit measures one memory write's commit: add, status,
// commit and rev-parse (ADR-0033 compares it with go-git).
func BenchmarkCommit(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("the git binary is not installed")
	}
	r, err := gitrepo.Open(context.Background(), filepath.Join(b.TempDir(), "ws"))
	if err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.Dir(), "memory"), 0o750); err != nil {
		b.Fatal(err)
	}
	for i := 0; b.Loop(); i++ {
		rel := "memory/fact-" + strconv.Itoa(i%200) + ".md"
		if err := os.WriteFile(filepath.Join(r.Dir(), rel), []byte("fact "+strconv.Itoa(i)), 0o600); err != nil {
			b.Fatal(err)
		}
		if _, err := r.Commit(context.Background(), []string{rel}, "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

// Ruling R37: a cancel does not kill git half-way (which would leave
// .git/index.lock and break every later write): the commit finishes, and
// the next one works.
// The turn's context is cancelled here before the commit starts, the
// worst case of a cancel at any step of it.
func TestCancelDuringACommitLeavesTheRepositoryUsable(t *testing.T) {
	r := open(t)
	again := r
	write(t, again, "memory/a.md", "one")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := again.Commit(ctx, []string{"memory/a.md"}, "one"); err != nil {
		t.Fatalf("a cancelled commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index.lock left behind: %v", err)
	}
	write(t, again, "memory/b.md", "two")
	if _, err := again.Commit(context.Background(), []string{"memory/b.md"}, "two"); err != nil {
		t.Fatalf("the next commit: %v", err)
	}
	if got := gitOut(t, r.Dir(), "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("%s commits, want 2", got)
	}
}

// Ruling R37: a stale index.lock (a git killed before this start) is
// removed at Open, so memory works again.
func TestOpenRemovesAStaleIndexLock(t *testing.T) {
	r := open(t)
	lock := filepath.Join(r.Dir(), ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := gitrepo.Open(context.Background(), r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stale lock is still there: %v", err)
	}
	write(t, again, "memory/a.md", "one")
	if _, err := again.Commit(context.Background(), []string{"memory/a.md"}, "one"); err != nil {
		t.Fatal(err)
	}
}

func TestHeadIsTheLastCommit(t *testing.T) {
	r := open(t)
	if _, err := r.Head(context.Background()); err == nil {
		t.Fatal("a repository with no commit has a head")
	}
	write(t, r, "memory/a.md", "a")
	id, err := r.Commit(context.Background(), []string{"memory/a.md"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if head, err := r.Head(context.Background()); err != nil || head != id {
		t.Fatalf("head %q, %v; want %q", head, err, id)
	}
}

func TestRevertAllIsOneCommit(t *testing.T) {
	r := open(t)
	ctx := context.Background()
	write(t, r, "memory/a.md", "one\n")
	first, err := r.Commit(ctx, []string{"memory/a.md"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	write(t, r, "memory/b.md", "two\n")
	second, err := r.Commit(ctx, []string{"memory/b.md"}, "b")
	if err != nil {
		t.Fatal(err)
	}
	before := gitOut(t, r.Dir(), "rev-list", "--count", "HEAD")
	undo, err := r.RevertAll(ctx, []string{first, second}, "undo both\n\nUndo-Of: "+first+"\nUndo-Of: "+second+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if after := gitOut(t, r.Dir(), "rev-list", "--count", "HEAD"); after != strconv.Itoa(mustAtoi(t, before)+1) {
		t.Fatalf("commits %s → %s, want one more", before, after)
	}
	if head := gitOut(t, r.Dir(), "rev-parse", "HEAD"); head != undo {
		t.Fatalf("head %s, undo %s", head, undo)
	}
	for _, p := range []string{"memory/a.md", "memory/b.md"} {
		if _, err := os.Stat(filepath.Join(r.Dir(), p)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still there: %v", p, err)
		}
	}
	for _, c := range []string{first, second} {
		if ok, err := r.Reverted(ctx, c); err != nil || !ok {
			t.Fatalf("Reverted(%s) = %v %v", c, ok, err)
		}
	}
}

func TestRevertAllConflictChangesNothing(t *testing.T) {
	r := open(t)
	ctx := context.Background()
	write(t, r, "memory/a.md", "one\n")
	if _, err := r.Commit(ctx, []string{"memory/a.md"}, "a"); err != nil {
		t.Fatal(err)
	}
	write(t, r, "memory/a.md", "two\n")
	changed, err := r.Commit(ctx, []string{"memory/a.md"}, "a two")
	if err != nil {
		t.Fatal(err)
	}
	write(t, r, "memory/a.md", "three\n")
	later, err := r.Commit(ctx, []string{"memory/a.md"}, "a three")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RevertAll(ctx, []string{changed}, "undo\n\nUndo-Of: "+changed+"\n")
	if !errors.Is(err, gitrepo.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if head := gitOut(t, r.Dir(), "rev-parse", "HEAD"); head != later {
		t.Fatalf("head moved to %s", head)
	}
	if st := gitOut(t, r.Dir(), "status", "--porcelain"); st != "" {
		t.Fatalf("the work tree is not clean:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), ".git", "REVERT_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a revert is still in progress: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "memory/a.md")); string(b) != "three\n" {
		t.Fatalf("the later write is lost: %q", b)
	}
	if ok, err := r.Reverted(ctx, changed); err != nil || ok {
		t.Fatalf("Reverted = %v %v", ok, err)
	}
}

func TestRevertAllLeavesOtherChangesAlone(t *testing.T) {
	r := open(t)
	ctx := context.Background()
	write(t, r, "memory/a.md", "one\n")
	c, err := r.Commit(ctx, []string{"memory/a.md"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	write(t, r, "modules/x/src.rs", "draft\n")
	if _, err := r.RevertAll(ctx, []string{c}, "undo\n\nUndo-Of: "+c+"\n"); err != nil {
		t.Fatal(err)
	}
	if st := gitOut(t, r.Dir(), "status", "--porcelain", "--untracked-files=all"); st != "?? modules/x/src.rs" {
		t.Fatalf("status %q", st)
	}
}

func TestLastFindsAWholeLine(t *testing.T) {
	r := open(t)
	ctx := context.Background()
	write(t, r, "memory/a.md", "one\n")
	c, err := r.Commit(ctx, []string{"memory/a.md"}, "a\n\nChange-Key: k:1-2\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"Change-Key: k:1-2"} {
		if got, err := r.Last(ctx, line); err != nil || got != c {
			t.Fatalf("Last(%q) = %q %v, want %s", line, got, err, c)
		}
	}
	for _, line := range []string{"Change-Key: k:1", "Change-Key: k.1-2", "Key: k:1-2"} {
		if got, err := r.Last(ctx, line); err != nil || got != "" {
			t.Fatalf("Last(%q) = %q %v, want none", line, got, err)
		}
	}
	if _, err := r.RevertAll(ctx, []string{"--all"}, "x"); err == nil {
		t.Fatal("an option as a commit")
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLogDiffFileAt(t *testing.T) {
	ctx := context.Background()
	r := open(t)
	const rel = "config/ganglion.yaml"
	put := func(text, msg string) string {
		t.Helper()
		p, err := r.Path(rel)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		id, err := r.Commit(ctx, []string{rel}, msg)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if log, err := r.Log(ctx, rel, 10); err != nil || len(log) != 0 {
		t.Fatalf("an empty repository's Log = %+v, %v", log, err)
	}
	first := put("a: 1\n", "first\n\nSource: host")
	second := put("a: 2\n", "second\n\nSource: propose")

	log, err := r.Log(ctx, rel, 10)
	if err != nil || len(log) != 2 || log[0].ID != second || log[1].ID != first ||
		log[0].Message != "second\n\nSource: propose" || log[0].When.IsZero() {
		t.Fatalf("Log = %+v, %v", log, err)
	}
	if one, _ := r.Log(ctx, rel, 1); len(one) != 1 {
		t.Fatalf("Log(1) = %+v", one)
	}
	d, err := r.Diff(ctx, second, rel)
	if err != nil || !strings.Contains(d, "-a: 1") || !strings.Contains(d, "+a: 2") {
		t.Fatalf("Diff = %q, %v", d, err)
	}
	before, ok, err := r.FileAt(ctx, second, rel, true)
	if err != nil || !ok || string(before) != "a: 1\n" {
		t.Fatalf("FileAt parent = %q %v %v", before, ok, err)
	}
	after, ok, err := r.FileAt(ctx, second, rel, false)
	if err != nil || !ok || string(after) != "a: 2\n" {
		t.Fatalf("FileAt = %q %v %v", after, ok, err)
	}
	if _, ok, err = r.FileAt(ctx, first, rel, true); err != nil || ok {
		t.Fatalf("a root commit's parent has the file: %v %v", ok, err)
	}
	if _, err := r.Diff(ctx, "not-a-commit", rel); err == nil {
		t.Fatal("a bad commit id was passed to git")
	}
	if _, _, err := r.FileAt(ctx, second, "../x", false); err == nil {
		t.Fatal("a path outside was read")
	}
}
