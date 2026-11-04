package chatlab

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultRepeat is how many times a scenario runs unless it says otherwise.
const DefaultRepeat = 3

// PersonaDefault is the example persona in the repository.
const PersonaDefault = "default"

// ModelScripted runs a scenario on a fake model that plays its script.
const ModelScripted = "scripted"

// Scenario is one check, as data (schemas/chatlab-scenario.schema.json).
type Scenario struct {
	Name    string       `yaml:"name"`
	Why     string       `yaml:"why"` // the bug it guards against
	Repeat  int          `yaml:"repeat"`
	Persona string       `yaml:"persona"` // PersonaDefault or a directory under the scenario's
	Skills  []string     `yaml:"skills"`  // directories copied to workspace/skills
	Model   string       `yaml:"model"`   // "" or ModelScripted
	Script  []ScriptStep `yaml:"script"`
	Pages   []Page       `yaml:"pages"`
	Steps   []Step       `yaml:"steps"`
	Dir     string       `yaml:"-"` // where the scenario's file is
}

// ScriptStep is one answer of the scripted model: words or a tool call.
type ScriptStep struct {
	Say  string `yaml:"say"`
	Tool string `yaml:"tool"`
	Args string `yaml:"args"` // JSON
}

// Page is a web page the node reaches at URL, served from File.
type Page struct {
	URL  string `yaml:"url"`
	File string `yaml:"file"`
}

// Step is one action; exactly one field is set.
type Step struct {
	Say     string   `yaml:"say"`
	Press   string   `yaml:"press"`
	Judge   string   `yaml:"judge"` // a claim about the model's last words
	Send    *File    `yaml:"send"`
	Advance *Advance `yaml:"advance"`
	Reach   *Reach   `yaml:"reach"`
	Facts   *Facts   `yaml:"facts"`
}

// Advance moves one of the node's clocks forward.
type Advance struct {
	Confirmations time.Duration `yaml:"confirmations"`
	Scheduler     time.Duration `yaml:"scheduler"`
}

// Reach declares a state the scenario needs. A real model can skip a tool;
// without the state, the repeat says nothing and does not count.
type Reach struct {
	Journal JournalFact `yaml:"journal"`
	Nudge   string      `yaml:"nudge"` // said once when the state is missing
}

// JournalFact matches rows of the tool-call journal.
type JournalFact struct {
	Tool           string `yaml:"tool"`
	Decision       string `yaml:"decision"`
	ResultContains string `yaml:"result_contains"`
	Count          *int   `yaml:"count"` // nil: at least one
}

// Text is a check on a piece of text.
type Text struct {
	Contains    string `yaml:"contains"`
	NotContains string `yaml:"not_contains"`
}

// JobFact counts live scheduled jobs of a kind.
type JobFact struct {
	Kind  string `yaml:"kind"`
	Count int    `yaml:"count"`
}

// Facts is a closed list of checks on what the node did.
type Facts struct {
	Journal         []JournalFact `yaml:"journal"`
	NoJournal       []JournalFact `yaml:"no_journal"`
	Questions       *int          `yaml:"questions"`        // messages showing an Approve button now
	MessageReplaced string        `yaml:"message_replaced"` // replaced, no buttons, holds this text
	Reply           *Text         `yaml:"reply"`            // the model's words since the last action
	History         *Text         `yaml:"history"`          // the chat's stored history
	Memory          *Text         `yaml:"memory"`           // files under memory/, not quarantine
	Files           *int          `yaml:"files"`            // attachments shown so far
	Jobs            *JobFact      `yaml:"jobs"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// LoadScenario reads and checks one scenario.
func LoadScenario(path string) (Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("chatlab: %w", err)
	}
	var s Scenario
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("chatlab: %s: %w", path, err)
	}
	s.Dir = filepath.Dir(path)
	if s.Repeat == 0 {
		s.Repeat = DefaultRepeat
	}
	if s.Persona == "" {
		s.Persona = PersonaDefault
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if err := s.check(base); err != nil {
		return Scenario{}, fmt.Errorf("chatlab: %s: %w", path, err)
	}
	return s, nil
}

// LoadScenarios reads the scenarios of dir: those named, in that order, or
// all of them by name.
func LoadScenarios(dir string, names []string) ([]Scenario, error) {
	if len(names) == 0 {
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if err != nil {
			return nil, fmt.Errorf("chatlab: %w", err)
		}
		slices.Sort(files)
		for _, f := range files {
			names = append(names, strings.TrimSuffix(filepath.Base(f), ".yaml"))
		}
	}
	out := make([]Scenario, 0, len(names))
	for _, n := range names {
		if !nameRE.MatchString(n) {
			return nil, fmt.Errorf("chatlab: scenario name %q", n)
		}
		s, err := LoadScenario(filepath.Join(dir, n+".yaml"))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (s Scenario) check(base string) error {
	switch {
	case !nameRE.MatchString(s.Name):
		return fmt.Errorf("name %q must match %s", s.Name, nameRE)
	case s.Name != base:
		return fmt.Errorf("name %q differs from the file %q", s.Name, base)
	case strings.TrimSpace(s.Why) == "":
		return errors.New("why must name the bug this scenario guards against")
	case s.Repeat < 1:
		return fmt.Errorf("repeat %d", s.Repeat)
	}
	return errors.Join(s.checkModel(), s.checkPaths(), s.checkSteps())
}

func (s Scenario) checkModel() error {
	switch s.Model {
	case "":
		if len(s.Script) > 0 {
			return errors.New("script needs model: scripted")
		}
	case ModelScripted:
		if len(s.Script) == 0 {
			return errors.New("model: scripted needs a script")
		}
		for i, st := range s.Script {
			if (st.Say == "") == (st.Tool == "") {
				return fmt.Errorf("script step %d must have say or tool", i+1)
			}
		}
	default:
		return fmt.Errorf("model %q", s.Model)
	}
	return nil
}

func (s Scenario) checkPaths() error {
	for _, p := range s.Pages {
		if err := checkPageURL(p.URL); err != nil {
			return err
		}
		if err := inside(p.File); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(s.Dir, p.File)); err != nil {
			return fmt.Errorf("page %s: %w", p.File, err)
		}
	}
	dirs := slices.Clone(s.Skills)
	if s.Persona != PersonaDefault {
		dirs = append(dirs, s.Persona)
	}
	for _, d := range dirs {
		if err := inside(d); err != nil {
			return err
		}
	}
	return nil
}

// inside refuses a path that leaves the scenario's directory.
func inside(rel string) error {
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("path %q is outside the scenario's directory", rel)
	}
	return nil
}

// checkPageURL keeps test pages at public-looking names: every address check
// of the fetcher runs on them as in production.
func checkPageURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("page URL: %w", err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("page URL %q must be http: the fetcher trusts no test certificate", raw)
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("page URL %q is on loopback", raw)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return fmt.Errorf("page URL %q names a loopback or other address, not a host", raw)
	}
	return nil
}

func (s Scenario) checkSteps() error {
	canFail := false
	for i, st := range s.Steps {
		if err := st.check(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		if st.Judge != "" && s.Model == ModelScripted {
			return fmt.Errorf("step %d: judge has nothing to read on a scripted model", i+1)
		}
		canFail = canFail || st.Judge != "" || st.Facts != nil
	}
	if !canFail {
		return errors.New("no facts or judge step: this scenario cannot fail")
	}
	return nil
}

func (st Step) check() error {
	set := 0
	for _, on := range []bool{st.Say != "", st.Press != "", st.Judge != "", st.Send != nil, st.Advance != nil, st.Reach != nil,
		st.Facts != nil && !st.Facts.empty()} {
		if on {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("a step has one action, this one %d", set)
	}
	switch {
	case st.Advance != nil && (st.Advance.Confirmations > 0) == (st.Advance.Scheduler > 0):
		return errors.New("advance moves one clock forward")
	case st.Reach != nil && st.Reach.Journal.Tool == "":
		return errors.New("reach needs journal.tool")
	case st.Send != nil && st.Send.Kind != KindDocument && st.Send.Kind != KindPhoto:
		return fmt.Errorf("file kind %q", st.Send.Kind)
	}
	return nil
}

func (f *Facts) empty() bool {
	return len(f.Journal) == 0 && len(f.NoJournal) == 0 && f.Questions == nil && f.MessageReplaced == "" &&
		f.Reply == nil && f.History == nil && f.Memory == nil && f.Files == nil && f.Jobs == nil
}
