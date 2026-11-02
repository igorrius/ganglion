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
