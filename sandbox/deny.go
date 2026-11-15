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
	".ssh", ".gnupg", ".netrc", ".git-credentials", ".pgpass", ".npmrc", ".pypirc", ".s3cfg", ".vault-token",
	".aws", ".azure", ".kube", ".docker/config.json", ".password-store", ".local/share/keyrings",
	".config/gcloud", ".config/gh", ".config/hub", ".config/scw", ".config/railway", ".railway", ".config/op",
	".config/rclone", ".cargo/credentials", ".cargo/credentials.toml", ".terraform.d/credentials.tfrc.json",
	".m2/settings.xml", ".claude/.credentials.json", ".codex",
	".mozilla", ".config/google-chrome", ".config/chromium", ".config/BraveSoftware",
	".bash_history", ".zsh_history", ".local/share/fish/fish_history", ".python_history", ".psql_history",
}

// homeAutorun are files and folders whose contents run later outside the
// sandbox, at the owner's next shell or login: a command that wrote them
// would leave it.
var homeAutorun = []string{
	".bashrc", ".bash_profile", ".bash_login", ".profile", ".zshrc", ".zshenv", ".zprofile", ".zlogin",
	".config/fish", ".config/systemd", ".local/share/systemd", ".config/autostart", ".config/environment.d",
}

// skipped are folders a search for .env files does not enter: big, and
// not where an owner keeps one.
var skipped = []string{".git", "node_modules", ".cache", ".cargo", ".rustup", "go"}

// NodeFiles are the node's own files: its binary, data, settings file,
// logs, modules, core releases and the claude CLI work folder (its prompts and, during a
// call, the MCP bridge socket) and workspace.
type NodeFiles struct {
	Binary, DB, Settings, LogDir, ModulesDir, ClaudeDir, Workspace, Languages string
	// Releases is the folder of the node's core releases (ADR-0081): a
	// command that wrote a binary there would have it run, unconfined, as
	// the node at its next start.
	Releases string
	// MasterKey is the secret store's key file (ADR-0077); "" when the
	// node keeps none.
	MasterKey string
}

// HomeSecrets is the default list under home: the secret stores and the
// files that run at the owner's next login; none when home is unknown.
func HomeSecrets(home string) []string {
	if home == "" {
		return nil
	}
	out := make([]string, 0, len(homeSecrets)+len(homeAutorun))
	for _, p := range slices.Concat(homeSecrets, homeAutorun) {
		out = append(out, filepath.Join(home, p))
	}
	return out
}

// DenyList is every path a command may neither read nor write: the node's
// binary (a command that replaced it would run unconfined as the next
// helper), its data folder (or, when the database lies in the working
// folder, home or "/", the database with its journal files and its lock),
// its settings file wherever it lies (a command that rewrote it would set
// the admins, the providers' commands and the deny list at the next
// restart), logs, modules,
// workspace and language files, the secret store's key file (it lies beside
// the database, which is not always in a denied folder), each <NAME>_FILE secret in environ, the
// owner's secret stores and autorun files under home, and extra. A path that
// does not exist yet is kept: it is denied when it appears. Each is made
// absolute with the symlinks of its existing part resolved, since Landlock
// sees real paths; build the list again for each command, so a store that
// became a symlink is followed.
func DenyList(home, cwd string, node NodeFiles, environ, extra []string) []string {
	raw := slices.Concat([]string{node.Binary, node.Settings}, dataFiles(node.DB, cwd, home),
		[]string{node.LogDir, node.ModulesDir, node.ClaudeDir, node.Workspace, node.Languages, node.MasterKey, node.Releases})
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && strings.HasSuffix(name, "_FILE") && v != "" {
			raw = append(raw, v)
		}
	}
	raw = slices.Concat(raw, HomeSecrets(home), extra)
	var out []string
	for _, p := range raw {
		if p == "" {
			continue
		}
		if r, ok := resolve(p); ok && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// dataFiles is the database's folder, unless that folder is also where
// other work lives (the working folder, home, "/"): then the database, its
// journal files and its lock (node.LockDB), which a command that held it
// would keep the node from starting with.
func dataFiles(db, cwd, home string) []string {
	if db == "" {
		return nil
	}
	abs, err := filepath.Abs(db)
	if err != nil {
		return []string{db}
	}
	dir := filepath.Dir(abs)
	if dir != filepath.Clean(cwd) && dir != filepath.Clean(home) && dir != "/" {
		return []string{dir}
	}
	return []string{abs, abs + "-wal", abs + "-shm", abs + "-journal", abs + ".lock"}
}

// resolve is p absolute, with the symlinks of its longest existing prefix
// resolved and the rest appended as it is.
func resolve(p string) (string, bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	rest := ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest), true
		}
		if dir == "/" {
			return abs, true
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// envName reports whether a file's name is one of the environment files
// R-CMD-3 denies in any folder.
func envName(n string) bool { return n == ".env" || n == ".envrc" || strings.HasPrefix(n, ".env.") }

// EnvFiles finds .env, .env.* and .envrc files under roots down to depth
// folders, skipping big tool folders, symlinks included (the caller
// resolves them to what they point to): Landlock cannot deny by name, so
// they are denied by path, found at each command (R-CMD-3). A deeper one
// stays readable; the spike's note says why the depth is enough.
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
			case envName(n):
				out = append(out, p)
			case e.Type()&fs.ModeSymlink != 0:
			case e.IsDir():
				if left > 1 && !slices.Contains(skipped, n) {
					walk(p, left-1)
				}
			}
		}
	}
	for _, r := range roots {
		walk(r, depth)
	}
	return out
}
