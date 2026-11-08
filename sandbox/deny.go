package sandbox

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// homeSecrets are the owner's secret stores under the home folder that every
// command is denied (R-CMD-3); the owner adds more with --sandbox-deny.
var homeSecrets = []string{
	".ssh", ".gnupg", ".netrc", ".git-credentials", ".pgpass", ".npmrc", ".pypirc",
	".aws", ".azure", ".kube", ".docker/config.json", ".password-store", ".local/share/keyrings",
	".config/gcloud", ".config/gh", ".config/scw", ".config/railway", ".railway",
	".claude/.credentials.json",
}

// skipped are folders a search for .env files does not enter: big, and
// not where an owner keeps one.
var skipped = []string{".git", "node_modules", ".cache", ".cargo", ".rustup", "go"}

// NodeFiles are the node's own files: its data, logs, modules and workspace.
type NodeFiles struct{ DB, LogDir, ModulesDir, Workspace, Languages string }

// HomeSecrets is the default list of secret stores under home.
func HomeSecrets(home string) []string {
	out := make([]string, 0, len(homeSecrets))
	for _, p := range homeSecrets {
		out = append(out, filepath.Join(home, p))
	}
	return out
}

// DenyList is every path a command may neither read nor write: the node's
// files (the database with its journal files), each <NAME>_FILE secret in
// environ, the owner's secret stores under home, and extra. Each is made
// absolute with its symlinks resolved, since Landlock sees real paths; one
// that does not exist is left out, and is reached by nothing a later split
// grants unless it is created under a granted folder (see Grants).
func DenyList(home string, node NodeFiles, environ, extra []string) []string {
	var raw []string
	if node.DB != "" {
		raw = append(raw, node.DB, node.DB+"-wal", node.DB+"-shm", node.DB+"-journal")
	}
	raw = append(raw, node.LogDir, node.ModulesDir, node.Workspace, node.Languages)
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && strings.HasSuffix(name, "_FILE") && v != "" {
			raw = append(raw, v)
		}
	}
	raw = append(append(raw, HomeSecrets(home)...), extra...)
	var out []string
	for _, p := range raw {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil && !slices.Contains(out, resolved) {
			out = append(out, resolved)
		}
	}
	return out
}

// EnvFiles finds .env and .env.* files under roots down to depth folders,
// skipping big tool folders: Landlock cannot deny by name, so they are
// denied by path, found at each command (R-CMD-3). A deeper one stays
// readable; the research note of the spike says why the depth is enough.
func EnvFiles(roots []string, depth int, list Lister) []string {
	var out []string
	var walk func(dir string, left int)
	walk = func(dir string, left int) {
		entries, err := list(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			switch n := e.Name(); {
			case e.Type()&fs.ModeSymlink != 0:
			case e.IsDir():
				if left > 1 && !slices.Contains(skipped, n) {
					walk(p, left-1)
				}
			case n == ".env" || strings.HasPrefix(n, ".env."):
				out = append(out, p)
			}
		}
	}
	for _, r := range roots {
		walk(r, depth)
	}
	return out
}
