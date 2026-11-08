package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/procgroup"
	"github.com/igorrius/ganglion/sandbox"
)

// ignoredWait bounds one git check, so a question is never slow.
const ignoredWait = 5 * time.Second

// Ignored tells whether git ignores a file a content command names, for the
// warning in its question (R-CMD-6). It asks git in the sandbox, with no
// hooks and no file system monitor: git run outside it would let a hostile
// repository's core.fsmonitor start a program with the node's environment.
// Any doubt is "not ignored", which only leaves out a warning, never lets
// anything run.
type Ignored struct{ start Starter }

// NewIgnored returns the check over the sandbox.
func NewIgnored(start Starter) *Ignored { return &Ignored{start: start} }

// Ignored returns the repository of the first file in argv[1:] that git
// ignores there. A relative path is read against dir; an argument that is an
// option or not a regular file is skipped.
func (g *Ignored) Ignored(ctx context.Context, dir string, argv []string) (string, bool) {
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		path := a
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			continue
		}
		repo, ok := repoOf(filepath.Dir(path))
		if ok && g.check(ctx, repo, path) {
			return repo, true
		}
	}
	return "", false
}

// repoOf is the nearest folder at or above dir with a .git entry.
func repoOf(dir string) (string, bool) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		if d == filepath.Dir(d) {
			return "", false
		}
	}
}

// check asks git whether it ignores path in repo: exit 0 is ignored. The
// command gets no input, no output and no settings but the repository's.
func (g *Ignored) check(ctx context.Context, repo, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, ignoredWait)
	defer cancel()
	cmd, err := g.start.Command(ctx, []string{"git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"check-ignore", "-q", "--", path}, repo, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if err != nil {
		return false
	}
	procgroup.Own(cmd)
	err = cmd.Start()
	sandbox.Release(cmd)
	if err != nil {
		return false
	}
	defer procgroup.Kill(cmd)
	return cmd.Wait() == nil
}
