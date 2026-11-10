package builder

import (
	"path"
	"regexp"
	"strings"
)

// NoOwnDiagnostic is what OwnDiagnostics answers when nothing in the log is
// about the module's own files.
const NoOwnDiagnostic = "the build failed; no diagnostic names the module's own files"

var (
	// diagHeader starts a diagnostic: rustc's and cargo's "error",
	// "error[E0308]" or "warning" at the start of a line.
	diagHeader = regexp.MustCompile(`^(error|warning)(\[[A-Za-z0-9]+\])?:`)
	// depWarning is a line a dependency's build script printed through
	// cargo: "warning: <crate>@<version>: …".
	depWarning = regexp.MustCompile(`^warning: [^\s@:]+@[^\s:]+:`)
	// placeLine is a diagnostic's place: " --> path:line:col" or
	// " ::: path:line:col".
	placeLine = regexp.MustCompile(`^\s*(?:-->|:::)\s+(.+):\d+:\d+\s*$`)
	// fromMacro marks a diagnostic raised inside a macro's expansion, whose
	// message a dependency's macro may have written.
	fromMacro = regexp.MustCompile(`originates in the (?:[a-z]+ )?macro`)
)

// stdPrefix is where rustc places the standard library's files: the
// toolchain's, never a dependency's (those are under the build's cargo home).
const stdPrefix = "/rustc/"

// OwnDiagnostics keeps rustc's diagnostics about the module's own files
// (ruling 8 of M4c, R-RUN-17): the blocks that start with "error" or
// "warning" and whose every place is the module's src/, Cargo.toml or
// manifest.yaml (or the standard library), with at least one of its own.
// A dependency's build script line ("warning: <crate>@<version>:"), a block
// raised inside a macro, a block that names another file and cargo's own
// lines are dropped. When nothing is kept it answers NoOwnDiagnostic.
func OwnDiagnostics(log Log) string {
	var kept []string
	for _, b := range diagBlocks(string(log)) {
		if ownBlock(b) {
			kept = append(kept, strings.Join(b, "\n"))
		}
	}
	if len(kept) == 0 {
		return NoOwnDiagnostic
	}
	return strings.Join(kept, "\n\n")
}

// diagBlocks splits text into diagnostics: each from a header line to the
// next blank line or header. Lines outside a diagnostic are left out.
func diagBlocks(text string) [][]string {
	var out [][]string
	var cur []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.TrimSpace(line) == "":
			out, cur = appendBlock(out, cur), nil
		case diagHeader.MatchString(line):
			out, cur = appendBlock(out, cur), []string{line}
		case cur != nil:
			cur = append(cur, line)
		}
	}
	return appendBlock(out, cur)
}

// appendBlock is out with b when b holds a line.
func appendBlock(out [][]string, b []string) [][]string {
	if len(b) == 0 {
		return out
	}
	return append(out, b)
}

// ownBlock reports whether the diagnostic b is about the module's own files
// alone.
func ownBlock(b []string) bool {
	if depWarning.MatchString(b[0]) {
		return false
	}
	own := false
	for _, line := range b {
		if fromMacro.MatchString(line) {
			return false
		}
		m := placeLine.FindStringSubmatch(line)
		switch {
		case m == nil:
		case ownPath(m[1]):
			own = true
		case !strings.HasPrefix(m[1], stdPrefix):
			return false
		}
	}
	return own
}

// ownPath reports whether p, as cargo prints it relative to the build's
// copy, is one of the module's own files: under src/, Cargo.toml or
// manifest.yaml, with no "." or ".." on the way.
func ownPath(p string) bool {
	switch {
	case p == "Cargo.toml", p == "manifest.yaml":
		return true
	case strings.HasPrefix(p, "src/"):
		return path.Clean(p) == p
	}
	return false
}
