package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// gitDirect runs argv as it is, outside any sandbox, with the environment
// the check asks for: enough to test which file the check asks git about;
// the sandbox's own tests cover the confinement.
type gitDirect struct{ envs [][]string }

func (g *gitDirect) Command(ctx context.Context, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	g.envs = append(g.envs, env)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}, env...)
	return cmd, nil
}

// ignoredRepo is a git repository whose .gitignore holds .env.local, with
// that file and a README in it.
func ignoredRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the git binary is not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for name, body := range map[string]string{".gitignore": ".env.local\n", ".env.local": "KEY=v\n", "README": "hi\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// R-CMD-6: a content command naming a file git ignores gets the warning,
// with the repository; any other file does not.
func TestIgnoredFindsAFileGitIgnores(t *testing.T) {
	repo := ignoredRepo(t)
	g := &gitDirect{}
	ig := NewIgnored(g)
	if got, ok := ig.Ignored(t.Context(), repo, []string{"cat", ".env.local"}); !ok || got != repo {
		t.Fatalf("cat .env.local: %q %v, want %q", got, ok, repo)
	}
	if got, ok := ig.Ignored(t.Context(), "/", []string{"cat", "-n", filepath.Join(repo, ".env.local")}); !ok || got != repo {
		t.Fatalf("cat -n <abs>/.env.local: %q %v, want %q", got, ok, repo)
	}
	if _, ok := ig.Ignored(t.Context(), repo, []string{"cat", "README"}); ok {
		t.Fatal("cat README is ignored")
	}
	// git is asked with no system or user settings.
	if len(g.envs) == 0 || !slices.Contains(g.envs[0], "GIT_CONFIG_NOSYSTEM=1") || !slices.Contains(g.envs[0], "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("git's environment %v", g.envs)
	}
}

func TestIgnoredOutsideARepositoryOrMissingIsNot(t *testing.T) {
	repo := ignoredRepo(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, ".env.local"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &gitDirect{}
	ig := NewIgnored(g)
	if _, ok := ig.Ignored(t.Context(), outside, []string{"cat", ".env.local"}); ok {
		t.Fatal("a file outside any repository is ignored")
	}
	if _, ok := ig.Ignored(t.Context(), repo, []string{"cat", ".env.missing"}); ok {
		t.Fatal("a missing file is ignored")
	}
	if _, ok := ig.Ignored(t.Context(), repo, []string{"cat", "."}); ok {
		t.Fatal("a folder is ignored")
	}
}

// A sandbox that is off starts nothing: no warning, and nothing runs.
func TestIgnoredWithTheSandboxOffIsNot(t *testing.T) {
	repo := ignoredRepo(t)
	if _, ok := NewIgnored(offStarter{}).Ignored(t.Context(), repo, []string{"cat", ".env.local"}); ok {
		t.Fatal("ignored with no sandbox")
	}
}
