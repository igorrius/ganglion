package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// MaxPersonaBytes bounds the persona: it goes into every model call.
const MaxPersonaBytes = 32 << 10

// htmlComment matches <!-- … -->: notes for the operator editing the file,
// which never reach the model.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// PersonaDir returns a Persona source over <workspace>/persona: every *.md
// file there, in name order, without HTML comments, joined by blank lines
// (ADR-0024). The operator splits the description as they like (identity,
// style, people); importers from other assistants write the same files. A
// missing directory is no persona.
func PersonaDir(workspace string) PersonaFunc {
	dir := filepath.Join(workspace, "persona")
	return func() (string, error) {
		names, err := personaFiles(dir)
		if err != nil {
			return "", err
		}
		var parts []string
		size := 0
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				return "", fmt.Errorf("persona: %w", err)
			}
			if size += len(b); size > MaxPersonaBytes {
				return "", fmt.Errorf("persona: %s is over %d KiB in total; it goes into every model call", dir, MaxPersonaBytes>>10)
			}
			if t := strings.TrimSpace(htmlComment.ReplaceAllString(string(b), "")); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n"), nil
	}
}

// personaFiles lists the *.md files of dir in name order; a missing dir
// has none.
func personaFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("persona: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}
