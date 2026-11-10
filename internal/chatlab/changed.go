package chatlab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SharedFiles touch every reply: the main model, the rules and the way to the
// model. A change here recommends a full run; it never starts one, as that takes
// about 900,000 tokens and is decided, not automatic (owner, 2026-11-04).
var SharedFiles = []string{
	"examples/ganglion.lab.yaml",
	"agent/prompt.go",
	"agent/stages.go",
	"agent/compact.go",
	"examples/workspace/persona/**",
	"provider/**",
	"mcp/**",
}

// Record is what the last green runs saw, in tools/chatlab/passed.json. It is in
// git, so a fresh clone does not spend a full run on finding out, and a commit
// shows which checks ran again.
type Record struct {
	Scenarios map[string]string `json:"scenarios"` // a passed scenario's fingerprint
	Shared    map[string]string `json:"shared"`    // SharedFiles at the last full run or decision
}

// LoadRecord reads the record. None, or one that does not parse, is an empty
// record; see Empty.
func LoadRecord(path string) (Record, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, fmt.Errorf("chatlab: %w", err)
	}
	var r Record
	if json.Unmarshal(b, &r) != nil {
		return Record{}, nil
	}
	return r, nil
}

// Save writes the record.
func (r Record) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("chatlab: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("chatlab: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil { //nolint:gosec // a file of the repository
		return fmt.Errorf("chatlab: %w", err)
	}
	return nil
}

// Empty reports a record that knows nothing. Selecting on it would start a full
// run, which is decided and never automatic.
func (r Record) Empty() bool { return len(r.Scenarios) == 0 }

// Passed remembers s as green on the files as they are now.
func (r *Record) Passed(root string, s Scenario) error {
	fp, err := fingerprint(root, s)
	if err != nil {
		return err
	}
	if r.Scenarios == nil {
		r.Scenarios = map[string]string{}
	}
	r.Scenarios[s.Name] = fp
	return nil
}

// Failed forgets s, so it runs next time.
func (r *Record) Failed(s Scenario) { delete(r.Scenarios, s.Name) }

// Select splits scenarios into those to run, because something of theirs changed
// since they last passed, and the names of the rest.
func Select(root string, all []Scenario, r Record) (run []Scenario, untouched []string, err error) {
	for _, s := range all {
		fp, err := fingerprint(root, s)
		if err != nil {
			return nil, nil, err
		}
		if r.Scenarios[s.Name] == fp {
			untouched = append(untouched, s.Name)
			continue
		}
		run = append(run, s)
	}
	return run, untouched, nil
}

// FullRunReasons lists what changed in shared since the last full run, or since
// it was decided to do without one.
func (r Record) FullRunReasons(root string, shared []string) ([]string, error) {
	if r.Shared == nil {
		return []string{"no full run yet"}, nil
	}
	now, err := hashes(root, shared)
	if err != nil {
		return nil, err
	}
	var out []string
	for path, sum := range now {
		if r.Shared[path] != sum {
			out = append(out, path)
		}
	}
	for path := range r.Shared {
		if _, ok := now[path]; !ok {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out, nil
}

// FullRunDone clears the recommendation: a full run passed, or it was decided
// not to make one.
func (r *Record) FullRunDone(root string, shared []string) error {
	now, err := hashes(root, shared)
	if err != nil {
		return err
	}
	r.Shared = now
	return nil
}

// fingerprint is the hash of everything s depends on: its own file, pages, skills
// and persona, and the files it covers. SharedFiles are not part of it.
func fingerprint(root string, s Scenario) (string, error) {
	files, err := s.files(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			return "", fmt.Errorf("chatlab: %s: %w", s.Name, err)
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f.label, len(b))
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type scenarioFile struct{ label, path string }

// files are what a scenario's fingerprint reads, in a fixed order.
func (s Scenario) files(root string) ([]scenarioFile, error) {
	out := []scenarioFile{{s.Name + ".yaml", filepath.Join(s.Dir, s.Name+".yaml")}}
	for _, p := range s.Pages {
		out = append(out, scenarioFile{p.File, filepath.Join(s.Dir, p.File)})
	}
	dirs := slices.Clone(s.Skills)
	if s.Persona != "" && s.Persona != PersonaDefault {
		dirs = append(dirs, s.Persona)
	}
	for _, d := range dirs {
		files, err := matchFiles(s.Dir, d+"/**")
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			out = append(out, scenarioFile{f, filepath.Join(s.Dir, f)})
		}
	}
	for _, pattern := range s.Covers {
		files, err := matchFiles(root, pattern)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("chatlab: %s: covers %q matches no file", s.Name, pattern)
		}
		for _, f := range files {
			out = append(out, scenarioFile{f, filepath.Join(root, f)})
		}
	}
	return out, nil
}

func hashes(root string, patterns []string) (map[string]string, error) {
	out := map[string]string{}
	for _, pattern := range patterns {
		files, err := matchFiles(root, pattern)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(root, f))
			if err != nil {
				return nil, fmt.Errorf("chatlab: %w", err)
			}
			sum := sha256.Sum256(b)
			out[f] = hex.EncodeToString(sum[:])
		}
	}
	return out, nil
}

// matchFiles lists the files of a pattern under root, by slash-separated paths,
// without tests: "dir/**" is a whole tree, anything else is a glob.
func matchFiles(root, pattern string) ([]string, error) {
	var paths []string
	var err error
	if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
		paths, err = walkFiles(filepath.Join(root, dir))
	} else {
		paths, err = filepath.Glob(filepath.Join(root, pattern))
	}
	if err != nil {
		return nil, fmt.Errorf("chatlab: covers %q: %w", pattern, err)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil, fmt.Errorf("chatlab: %w", err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

// walkFiles lists the files under dir; none when it does not exist.
func walkFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}
