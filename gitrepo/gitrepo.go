// Package gitrepo keeps the agent's workspace in a git repository: every
// change to memory (and later the persona) is a commit, so a bad entry is
// undone with a revert (R-MEM-3, ADR-0024, ADR-0033). It drives the git
// binary; it never reads or changes the user's or the system's git
// configuration, and it never follows git's environment variables to
// another repository.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Errors; callers decide on them with errors.Is.
var (
	// ErrNoGit is a host without the git binary.
	ErrNoGit = errors.New("gitrepo: the git binary is not installed; the workspace repository needs it")
	// ErrOutside is a path that is not a plain file path inside the
	// repository: absolute, climbing out with "..", empty, or inside .git.
	ErrOutside = errors.New("gitrepo: path outside the repository")
)

// The author of every commit; git's configuration is never consulted for it.
const (
	authorName  = "ganglion"
	authorEmail = "ganglion@localhost"
)

// commitPattern is a full or abbreviated commit id.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// Repo is a workspace directory under git. Its methods are safe for
// concurrent use: changes are made one at a time.
type Repo struct {
	dir string // absolute
	git string // the git binary
	mu  sync.Mutex
}

// Open returns the repository in dir, creating the directory and running
// git init when it is not a repository yet. Only dir itself counts: a
// repository around it (a checkout the workspace sits in) does not.
// ErrNoGit means the git binary is missing.
func Open(ctx context.Context, dir string) (*Repo, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("gitrepo: open: no directory given")
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoGit, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: open %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("gitrepo: open %s: %w", abs, err)
	}
	r := &Repo{dir: abs, git: bin}
	if _, err := os.Stat(filepath.Join(abs, ".git")); errors.Is(err, fs.ErrNotExist) {
		if _, err := r.run(ctx, "init", "--quiet"); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("gitrepo: open %s: %w", abs, err)
	}
	return r, nil
}

// Dir is the repository's absolute directory.
func (r *Repo) Dir() string { return r.dir }

// Path returns the absolute path of rel, a slash-separated path inside
// the repository; ErrOutside when rel is not one.
func (r *Repo) Path(rel string) (string, error) {
	if err := checkPath(rel); err != nil {
		return "", err
	}
	return filepath.Join(r.dir, filepath.FromSlash(rel)), nil
}

// checkPath refuses a path that is not a plain relative path inside the
// repository and outside .git.
func checkPath(rel string) error {
	local := filepath.FromSlash(rel)
	if rel == "" || !filepath.IsLocal(local) || strings.HasPrefix(rel, "-") {
		return fmt.Errorf("%w: %q", ErrOutside, rel)
	}
	first, _, _ := strings.Cut(filepath.ToSlash(filepath.Clean(local)), "/")
	if strings.EqualFold(first, ".git") {
		return fmt.Errorf("%w: %q", ErrOutside, rel)
	}
	return nil
}

// Commit records the current state of paths (added, changed or removed
// files) as one commit with msg and returns the commit id. Other changes
// in the working tree stay out of it. When paths hold no change, nothing
// is committed and the current commit is returned, so a rerun of the same
// write is not an error.
func (r *Repo) Commit(ctx context.Context, paths []string, msg string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("gitrepo: commit: no paths")
	}
	for _, p := range paths {
		if err := checkPath(p); err != nil {
			return "", err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	args := append([]string{"add", "--all", "--"}, paths...)
	if _, err := r.run(ctx, args...); err != nil {
		return "", err
	}
	changed, err := r.staged(ctx, paths)
	if err != nil {
		return "", err
	}
	if changed {
		args = append([]string{"commit", "--quiet", "--no-verify", "--message", msg, "--"}, paths...)
		if _, err := r.run(ctx, args...); err != nil {
			return "", err
		}
	}
	return r.head(ctx)
}

// staged reports whether paths hold a change for the next commit. They
// were just added, so any status line is a staged change; this works in
// a repository without commits too.
func (r *Repo) staged(ctx context.Context, paths []string) (bool, error) {
	out, err := r.run(ctx, append([]string{"status", "--porcelain", "--untracked-files=no", "--"}, paths...)...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// head is the id of the current commit.
func (r *Repo) head(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Revert undoes commit with a new commit and returns its id: how the
// owner takes back what the agent wrote (R-MEM-3).
func (r *Repo) Revert(ctx context.Context, commit string) (string, error) {
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf("gitrepo: revert: %q is not a commit id", commit)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.run(ctx, "revert", "--no-edit", commit); err != nil {
		return "", err
	}
	return r.head(ctx)
}

// run runs git in the repository with the fixed identity and without the
// user's, the system's or the environment's git settings, and returns its
// output.
func (r *Repo) run(ctx context.Context, args ...string) (string, error) {
	full := append([]string{
		"-c", "user.name=" + authorName, "-c", "user.email=" + authorEmail,
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull,
		"-C", r.dir,
	}, args...)
	cmd := exec.CommandContext(ctx, r.git, full...)
	cmd.Env = environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gitrepo: git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// environ is the process environment without git's own variables, with
// the global and system configuration turned off and prompts disabled.
func environ() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}
