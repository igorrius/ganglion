// Package skills loads the agent's skills (R-RTR-4, R-RTR-5, ADR-0042):
// Markdown procedures with a frontmatter (level 1), and scripts beside
// them (level 2). Built-in skills are embedded and reserved by name; the
// owner's live in workspace/skills/<name>/. A skill that breaks a rule is
// not loaded, and the reason is reported.
package skills

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/router"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.yaml.in/yaml/v3"
)

// Builtin holds the built-in skills, builtin/<name>/SKILL.md.
//
//go:embed builtin
var Builtin embed.FS

// Effects is what a script does: read runs by itself, change asks.
type Effects string

// Effect classes (ruling M4).
const (
	EffectsRead   Effects = "read"
	EffectsChange Effects = "change"
)

// Limits of a script's declaration.
const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 120 * time.Second
)

var skillName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// Script is a level-2 skill's script.
type Script struct {
	Run     string // file name in the skill's directory
	Effects Effects
	Env     []string // names passed through from the node's environment
	Timeout time.Duration
}

// Skill is one loaded skill.
type Skill struct {
	Name, Description, Body string
	Model                   router.TaskReq
	Script                  *Script
	Builtin                 bool
	Dir                     string // the skill's directory; "" for a built-in
}

// ToolName is the tool of a script skill: skill.<name with - → _>.
func (s Skill) ToolName() string { return "skill." + strings.ReplaceAll(s.Name, "-", "_") }

// Problem is a skill that was not loaded, and why: Reason for the log, Kind
// for /status, which shows no loader's text.
type Problem struct {
	Skill, Reason string
	Kind          ProblemKind
}

// ProblemKind is what kind of rule a skill broke.
type ProblemKind string

// The kinds of problems.
const (
	ProblemShape       ProblemKind = "shape"       // not a directory, or a symlink
	ProblemRead        ProblemKind = "read"        // SKILL.md cannot be read
	ProblemFrontmatter ProblemKind = "frontmatter" // the frontmatter breaks a rule
	ProblemScript      ProblemKind = "script"      // the script breaks a rule
	ProblemBuiltinName ProblemKind = "builtin"     // a built-in skill has its name
)

// loadError is a workspace skill's failure and its kind.
type loadError struct {
	kind ProblemKind
	err  error
}

func (e *loadError) Error() string { return e.err.Error() }
func (e *loadError) Unwrap() error { return e.err }

// Set is the loaded skills by name.
type Set struct {
	byName map[string]Skill
}

// Get returns a skill by name.
func (s Set) Get(name string) (Skill, bool) {
	sk, ok := s.byName[name]
	return sk, ok
}

// Names are the skills' names, sorted.
func (s Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Index is one "- name: description" line per skill, sorted: the system
// prompt's list, the same bytes every turn.
func (s Set) Index() string {
	lines := make([]string, 0, len(s.byName))
	for _, n := range s.Names() {
		lines = append(lines, "- "+n+": "+s.byName[n].Description)
	}
	return strings.Join(lines, "\n")
}

// frontmatter is SKILL.md's YAML header.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       struct {
		Tier    router.Tier `yaml:"tier"`
		Tools   bool        `yaml:"tools"`
		Context string      `yaml:"context"`
		Vision  bool        `yaml:"vision"`
		Privacy string      `yaml:"privacy"`
	} `yaml:"model"`
	Script *struct {
		Run     string   `yaml:"run"`
		Effects Effects  `yaml:"effects"`
		Env     []string `yaml:"env"`
		Timeout string   `yaml:"timeout"`
	} `yaml:"script"`
}

// Load reads the built-in skills from builtin (builtin/<name>/SKILL.md)
// and the owner's from workspace/skills. deny names variables a script
// may never get besides the fixed patterns: each provider's key_env.
func Load(builtin fs.FS, workspace string, deny []string) (Set, []Problem, error) {
	set := Set{byName: map[string]Skill{}}
	var problems []Problem
	entries, err := fs.ReadDir(builtin, "builtin")
	if err != nil {
		return Set{}, nil, fmt.Errorf("skills: built-in: %w", err)
	}
	for _, e := range entries {
		src, err := fs.ReadFile(builtin, path.Join("builtin", e.Name(), "SKILL.md"))
		if err != nil {
			return Set{}, nil, fmt.Errorf("skills: built-in %s: %w", e.Name(), err)
		}
		sk, err := parse(e.Name(), src)
		if err == nil && sk.Script != nil {
			err = errors.New("a built-in skill has no script")
		}
		if err != nil {
			return Set{}, nil, fmt.Errorf("skills: built-in %s: %w", e.Name(), err) // our own bug: fail the start
		}
		sk.Builtin = true
		set.byName[sk.Name] = sk
	}
	root := filepath.Join(workspace, "skills")
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return set, nil, nil
	}
	if err != nil {
		return Set{}, nil, fmt.Errorf("skills: %w", err)
	}
	for _, d := range dirs {
		sk, err := loadDir(root, d, deny)
		switch {
		case err != nil:
			problems = append(problems, Problem{Skill: d.Name(), Reason: err.Error(), Kind: kindOf(err)})
		case set.byName[sk.Name].Builtin:
			problems = append(problems, Problem{Skill: sk.Name, Reason: "a built-in skill has this name; a workspace skill cannot replace it",
				Kind: ProblemBuiltinName})
		default:
			set.byName[sk.Name] = sk
		}
	}
	return set, problems, nil
}

// loadDir reads one workspace skill and checks its script.
func loadDir(root string, d fs.DirEntry, deny []string) (Skill, error) {
	if !d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
		return Skill{}, &loadError{ProblemShape, errors.New("not a directory")}
	}
	dir, err := filepath.Abs(filepath.Join(root, d.Name())) // a script runs by its absolute path, from another directory
	if err != nil {
		return Skill{}, &loadError{ProblemRead, err}
	}
	src, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, &loadError{ProblemRead, err}
	}
	sk, err := parse(d.Name(), src)
	if err != nil {
		return Skill{}, &loadError{ProblemFrontmatter, err}
	}
	sk.Dir = dir
	if sk.Script != nil {
		if err := checkScript(dir, *sk.Script, deny); err != nil {
			return Skill{}, &loadError{ProblemScript, err}
		}
	}
	return sk, nil
}

// kindOf is a load failure's kind; frontmatter when it has none.
func kindOf(err error) ProblemKind {
	if le, ok := errors.AsType[*loadError](err); ok {
		return le.kind
	}
	return ProblemFrontmatter
}

// parse reads a SKILL.md: the frontmatter between "---" lines, then the
// body. The name must be the directory's.
func parse(dirName string, src []byte) (Skill, error) {
	src = bytes.ReplaceAll(bytes.TrimPrefix(src, []byte("\ufeff")), []byte("\r\n"), []byte("\n")) // saved on Windows
	rest, ok := bytes.CutPrefix(src, []byte("---\n"))
	head, body, found := bytes.Cut(rest, []byte("\n---\n"))
	if !ok || !found {
		return Skill{}, errors.New("SKILL.md must start with a --- frontmatter block")
	}
	var fm frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(head))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return Skill{}, fmt.Errorf("frontmatter: %w", err)
	}
	switch {
	case !skillName.MatchString(fm.Name):
		return Skill{}, fmt.Errorf("name %q: want %s", fm.Name, skillName)
	case fm.Name != dirName:
		return Skill{}, fmt.Errorf("name %q differs from its directory %q", fm.Name, dirName)
	case fm.Name == reservedName:
		return Skill{}, fmt.Errorf("name %q is reserved: its tool would be skill.read", reservedName)
	case strings.TrimSpace(fm.Description) == "":
		return Skill{}, errors.New("description is empty")
	}
	if err := checkModel(fm.Model.Tier, fm.Model.Context, fm.Model.Privacy); err != nil {
		return Skill{}, err
	}
	desc := strings.Join(strings.Fields(fm.Description), " ") // one line in the index
	sk := Skill{Name: fm.Name, Description: desc, Body: strings.TrimSpace(string(body)),
		Model: router.TaskReq{Tier: fm.Model.Tier, Tools: fm.Model.Tools, Context: fm.Model.Context, Vision: fm.Model.Vision,
			Privacy: fm.Model.Privacy}}
	if s := fm.Script; s != nil {
		sc := Script{Run: s.Run, Effects: s.Effects, Env: s.Env, Timeout: defaultTimeout}
		if s.Timeout != "" {
			d, err := time.ParseDuration(s.Timeout)
			if err != nil || d <= 0 || d > maxTimeout {
				return Skill{}, fmt.Errorf("script.timeout %q: want a duration up to %s", s.Timeout, maxTimeout)
			}
			sc.Timeout = d
		}
		if sc.Effects != EffectsRead && sc.Effects != EffectsChange {
			return Skill{}, fmt.Errorf("script.effects %q: want read or change", sc.Effects)
		}
		sk.Script = &sc
	}
	return sk, nil
}

// reservedName is the skill name whose tool would be skill.read.
const reservedName = "read"

// PrivacyLocalOnly keeps a turn on local models.
const PrivacyLocalOnly = "local-only"

// checkModel refuses a model block value the router would not read: a
// typo must not fail open (privacy: local would route to the cloud).
func checkModel(tier router.Tier, context, privacy string) error {
	switch {
	case tier != "" && tier != router.TierCheap && tier != router.TierSmart:
		return fmt.Errorf("model.tier %q: want cheap or smart", tier)
	case context != "" && context != "short" && context != "long":
		return fmt.Errorf("model.context %q: want short or long", context)
	case privacy != "" && privacy != PrivacyLocalOnly:
		return fmt.Errorf("model.privacy %q: want %s", privacy, PrivacyLocalOnly)
	}
	return nil
}

// fixedEnv are the variables every script gets; a skill cannot list them.
var fixedEnv = []string{"PATH", "HOME"}

// denied are the node's secret-looking names a script never gets.
var denied = regexp.MustCompile(`^(TELEGRAM_.*|GANGLION_.*|.*_KEY|.*_TOKEN|.*_SECRET|.*_PASSWORD|.*_FILE)$`)

// checkScript holds a script to its rules: a regular executable file in
// the skill's directory, not a symlink, neither it nor the directory
// writable by others; no denied variable.
func checkScript(dir string, s Script, deny []string) error {
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("the skill's directory is missing, a symlink, or writable by others")
	}
	if s.Run == "" || s.Run != filepath.Base(s.Run) || strings.HasPrefix(s.Run, ".") {
		return fmt.Errorf("script.run %q: a file name in the skill's directory", s.Run)
	}
	st, err := os.Lstat(filepath.Join(dir, s.Run))
	switch {
	case err != nil:
		return fmt.Errorf("script.run: %w", err)
	case !st.Mode().IsRegular():
		return fmt.Errorf("script.run %q is not a regular file (a symlink is refused)", s.Run)
	case st.Mode().Perm()&0o111 == 0:
		return fmt.Errorf("script.run %q is not executable", s.Run)
	case st.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("script.run %q is writable by others", s.Run)
	}
	for _, name := range s.Env {
		if denied.MatchString(name) || slices.Contains(deny, name) || slices.Contains(fixedEnv, name) {
			return fmt.Errorf("script.env %s: the node's secrets and %s are never passed", name, strings.Join(fixedEnv, ", "))
		}
	}
	return nil
}

// Report logs the skills that were not loaded, one warning each (the
// owner's own files, so the reasons are safe to log), and records how
// many are loaded, by source.
func Report(log *slog.Logger, set Set, problems []Problem) {
	for _, p := range problems {
		log.Warn("skills: not loaded", "skill", p.Skill, "reason", p.Reason)
	}
	g, _ := otel.Meter("ganglion/skills").Int64Gauge("ganglion.skills.loaded", metric.WithDescription("Skills loaded at start, by source."))
	counts := map[string]int64{"builtin": 0, "workspace": 0, "refused": int64(len(problems))}
	for _, sk := range set.byName {
		counts[map[bool]string{true: "builtin", false: "workspace"}[sk.Builtin]]++
	}
	for src, n := range counts {
		g.Record(context.Background(), n, metric.WithAttributes(attribute.String("source", src)))
	}
}
