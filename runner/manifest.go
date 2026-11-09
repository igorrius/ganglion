package runner

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/internal/size"
)

// Defaults for limits a manifest leaves out.
const (
	DefaultMemory  = 16 << 20
	DefaultTimeout = 20 * time.Second
	maxMemory      = 1 << 30
)

// Manifest declares what a module is, when it runs and what it may touch
// (docs/spec.md, section 7.5). Anything not listed is denied.
type Manifest struct {
	Name         string               `yaml:"name"`
	Version      int                  `yaml:"version"`
	ABI          int                  `yaml:"abi"` // the module ABI it speaks (ADR-0011); missing reads as 1
	Triggers     []Trigger            `yaml:"triggers"`
	Capabilities Capabilities         `yaml:"capabilities"`
	Cells        Cells                `yaml:"cells"`
	Config       map[string]ConfigKey `yaml:"config"` // the keys the module reads
	ConfigValues map[string]string    `yaml:"-"`      // their values, set by the host
	Limits       Limits               `yaml:"-"`
	Escalate     []Escalation         `yaml:"escalate"`
}

// Trigger starts a module: a cron schedule or a tool call by the model.
type Trigger struct {
	Cron   string `yaml:"cron,omitempty"`
	Tool   string `yaml:"tool,omitempty"`
	About  string `yaml:"about,omitempty"`  // the tool's description for the agent
	Output *Shape `yaml:"output,omitempty"` // required for a tool of a local module
}

// ConfigKey declares one setting a module reads.
type ConfigKey struct {
	Type     string `yaml:"type"` // string | number | boolean
	Required bool   `yaml:"required"`
	Default  string `yaml:"default"`
}

// CellDecl declares one cell a module reports.
type CellDecl struct {
	Kind  string `yaml:"kind"` // sensor (switch later)
	Type  Shape  `yaml:"type"`
	About string `yaml:"about"`
}

// Cells are the cells a module owns and the ones it may read.
type Cells struct {
	Report map[string]CellDecl `yaml:"report"` // by short name; the cell id is <module>.<name>
	Read   []string            `yaml:"read"`   // full cell ids
}

// Capabilities are the host functions a module is granted.
type Capabilities struct {
	HTTPFetch []string `yaml:"http.fetch"` // allowed host names
	SecretGet []string `yaml:"secret.get"` // allowed secret names
	EventEmit []string `yaml:"event.emit"` // allowed event kinds
	State     bool     `yaml:"state"`
}

// Limits bound one call of a module.
type Limits struct {
	Memory  uint64        // bytes
	Timeout time.Duration // per call
}

// Escalation names an outcome that calls the model.
type Escalation struct {
	On string `yaml:"on"`
}

// ParseManifest decodes a YAML manifest. Unknown fields are errors, so a
// typo cannot silently drop a restriction.
func ParseManifest(src []byte) (Manifest, error) {
	var raw struct {
		Manifest `yaml:",inline"`
		Limits   struct {
			Memory  string `yaml:"memory"`
			Timeout string `yaml:"timeout"`
		} `yaml:"limits"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	m := raw.Manifest
	if m.ABI == 0 {
		m.ABI = 1
	}
	if m.Name == "" {
		return Manifest{}, errors.New("manifest: name is required")
	}
	if err := m.checkV2(); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}

	m.Limits = Limits{Memory: DefaultMemory, Timeout: DefaultTimeout}
	if s := raw.Limits.Memory; s != "" {
		n, err := size.Parse(s)
		if err != nil {
			return Manifest{}, fmt.Errorf("manifest: limits.memory: %w", err)
		}
		if n > maxMemory {
			return Manifest{}, fmt.Errorf("manifest: limits.memory %s is above 1GiB", s)
		}
		m.Limits.Memory = n
	}
	if s := raw.Limits.Timeout; s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return Manifest{}, fmt.Errorf("manifest: limits.timeout %q is not a positive duration", s)
		}
		m.Limits.Timeout = d
	}
	return m, nil
}

var (
	cellNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)
	cellIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)+$`)
	toolNamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// checkV2 validates triggers, cells and config keys.
func (m Manifest) checkV2() error {
	for i, t := range m.Triggers {
		if (t.Cron == "") == (t.Tool == "") {
			return fmt.Errorf("trigger %d must set exactly one of cron, tool", i)
		}
		if t.Tool != "" && !toolNamePattern.MatchString(t.Tool) {
			return fmt.Errorf("tool %q: a name is lower-case letters, digits and _", t.Tool)
		}
	}
	for name, c := range m.Cells.Report {
		if !cellNamePattern.MatchString(name) {
			return fmt.Errorf("cells.report %q: a name is lower-case letters, digits, _ and -", name)
		}
		if c.Kind != "sensor" {
			return fmt.Errorf("cells.report %q: kind must be sensor", name)
		}
		if c.Type.Type == "" {
			return fmt.Errorf("cells.report %q: type is required", name)
		}
	}
	for _, id := range m.Cells.Read {
		if !cellIDPattern.MatchString(id) {
			return fmt.Errorf("cells.read %q: not a full cell id like module.cell", id)
		}
	}
	for name, k := range m.Config {
		switch k.Type {
		case "string", "number", "boolean":
		default:
			return fmt.Errorf("config %q: type must be string, number or boolean", name)
		}
	}
	return nil
}
