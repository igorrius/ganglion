package runner

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
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
	Name         string            `yaml:"name"`
	Version      int               `yaml:"version"`
	ABI          int               `yaml:"abi"` // the module ABI it speaks (ADR-0011); missing reads as 1
	Triggers     []Trigger         `yaml:"triggers"`
	Capabilities Capabilities      `yaml:"capabilities"`
	Config       map[string]string `yaml:"config"`
	Limits       Limits            `yaml:"-"`
	Escalate     []Escalation      `yaml:"escalate"`
}

// Trigger starts a module: a cron schedule or a tool call by the model.
type Trigger struct {
	Cron string `yaml:"cron,omitempty"`
	Tool string `yaml:"tool,omitempty"`
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
	for i, t := range m.Triggers {
		if (t.Cron == "") == (t.Tool == "") {
			return Manifest{}, fmt.Errorf("manifest: trigger %d must set exactly one of cron, tool", i)
		}
	}

	m.Limits = Limits{Memory: DefaultMemory, Timeout: DefaultTimeout}
	if s := raw.Limits.Memory; s != "" {
		n, err := parseSize(s)
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

// parseSize reads sizes such as "8MiB", "64KiB", "1GiB" or plain bytes.
func parseSize(s string) (uint64, error) {
	mult := uint64(1)
	for suffix, m := range map[string]uint64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30} {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			s, mult = rest, m
			break
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	if n > (1<<64-1)/mult {
		return 0, fmt.Errorf("size %q overflows", s)
	}
	return n * mult, nil
}
