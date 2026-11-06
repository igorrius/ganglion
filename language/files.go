package language

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/texts"
)

// LoadFiles reads an operator's language files, <code>.yaml, from dir (R-LANG-4). A file or a text
// that fails the checks is logged and left out, so a typo costs that text, never the start. A missing
// directory is no files.
func LoadFiles(dir string, log *slog.Logger) map[texts.Lang]map[texts.Key]string {
	out := map[texts.Lang]map[texts.Key]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("language: files not read", "dir", dir, "err", err)
		}
		return out
	}
	for _, e := range entries {
		name, isYAML := strings.CutSuffix(e.Name(), ".yaml")
		if e.IsDir() || !isYAML {
			continue
		}
		l, ok := texts.Normalize(name)
		if !ok || string(l) != name {
			log.Warn("language: file skipped: not a language in the table", "file", e.Name())
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Warn("language: file skipped", "file", e.Name(), "err", err)
			continue
		}
		file := map[texts.Key]string{}
		if err := yaml.Unmarshal(raw, &file); err != nil {
			log.Warn("language: file skipped", "file", e.Name(), "err", err)
			continue
		}
		good, rejected := texts.Check(texts.EnglishAll(), file)
		for _, k := range slices.Sorted(maps.Keys(rejected)) {
			log.Warn("language: text left out", "file", e.Name(), "key", string(k), "reason", rejected[k])
		}
		if len(good) > 0 {
			out[l] = good
		}
	}
	return out
}

// CheckOutside fails if dir is inside the workspace, where the agent's own tools could change what
// buttons say (R-LANG-4).
func CheckOutside(dir, workspace string) error {
	if workspace == "" {
		return nil // no agent, no workspace
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("language: %s: %w", dir, err)
	}
	w, err := filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("language: %s: %w", workspace, err)
	}
	rel, err := filepath.Rel(w, d)
	if err != nil {
		return nil // another volume: outside
	}
	if rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return fmt.Errorf("language: the language files %s are inside the workspace %s, where the agent could change them", d, w)
	}
	return nil
}
