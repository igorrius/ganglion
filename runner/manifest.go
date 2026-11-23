package runner

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/internal/size"
	"github.com/igorrius/ganglion/internal/text"
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
	Ports        Ports                `yaml:"ports"` // typed ends for links (R-BUS-5)
	Cells        Cells                `yaml:"cells"`
	Config       map[string]ConfigKey `yaml:"config"` // the keys the module reads
	ConfigValues map[string]string    `yaml:"-"`      // their values, set by the host
	Limits       Limits               `yaml:"-"`
	Escalate     []Escalation         `yaml:"escalate"`
}

// Trigger starts a module: a cron schedule, a tool call by the model or a
// change of a cell it listens to.
type Trigger struct {
	Cron   string   `yaml:"cron,omitempty"`
	Tool   string   `yaml:"tool,omitempty"`
	Cell   []string `yaml:"cell,omitempty"`   // full cell ids it listens to (R-RUN-15)
	HTTP   *HTTP    `yaml:"http,omitempty"`   // routes the edge's loopback listener serves (R-HTTP-1)
	About  string   `yaml:"about,omitempty"`  // the tool's description for the agent
	Output *Shape   `yaml:"output,omitempty"` // required for a tool of a local module
}

// HTTP is an http trigger: the routes a module answers on the node's
// loopback listener, under the module's own prefix (R-HTTP-1, ADR-0110).
type HTTP struct {
	Routes []Route `yaml:"routes"`
}

// Route is one path of an http trigger and the methods it answers.
type Route struct {
	Path    string   `yaml:"path"`
	Methods []string `yaml:"methods"`
}

// Listens is the union of the manifest's cell triggers, sorted ("" none).
func (m Manifest) Listens() []string {
	var ids []string
	for _, t := range m.Triggers {
		ids = append(ids, t.Cell...)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// MaxAbout caps a tool's description, which the manifest writes.
const MaxAbout = 300

// Description is the tool's about on one line, cut at MaxAbout: the text the
// agent reads as the tool's description and the install window shows ("" when
// the manifest gives none). The manifest writes it, so it is the module's text.
func (t Trigger) Description() string {
	return text.Truncate(strings.Join(strings.Fields(t.About), " "), MaxAbout)
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
	State     bool     `yaml:"state"`
	Notify    *Notify  `yaml:"notify,omitempty"` // may message the owner, at most PerDay a day
}

// Notify grants a module a daily budget of messages to the owner (R-RUN-16).
type Notify struct {
	PerDay int `yaml:"per_day"` // 1..MaxNotifyPerDay
}

// MaxNotifyPerDay caps what a manifest may ask for.
const MaxNotifyPerDay = 100

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
	if err := m.checkTriggers(); err != nil {
		return err
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
	if n := m.Capabilities.Notify; n != nil && (n.PerDay < 1 || n.PerDay > MaxNotifyPerDay) {
		return fmt.Errorf("notify.per_day must be 1..%d", MaxNotifyPerDay)
	}
	if err := m.Ports.check(); err != nil {
		return err
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

// checkTriggers validates each trigger and refuses a tool named twice (the
// install window would show one tool's about and the node run another's
// handler) or a cron line given twice (the module would run twice a tick).
func (m Manifest) checkTriggers() error {
	tools, crons, cells, http := map[string]bool{}, map[string]bool{}, false, false
	for i, t := range m.Triggers {
		if t.kinds() != 1 {
			return fmt.Errorf("trigger %d must set exactly one of cron, tool, cell, http", i)
		}
		if err := checkHTTP(i, t, &http); err != nil {
			return err
		}
		if err := m.checkCell(i, t, &cells); err != nil {
			return err
		}
		if t.Tool != "" && !toolNamePattern.MatchString(t.Tool) {
			return fmt.Errorf("tool %q: a name is lower-case letters, digits and _", t.Tool)
		}
		if t.Tool != "" && tools[t.Tool] {
			return fmt.Errorf("tool %s is declared twice", t.Tool)
		}
		cron := strings.Join(strings.Fields(t.Cron), " ")
		if cron != "" && crons[cron] {
			return fmt.Errorf("trigger %d repeats the cron line of another", i)
		}
		tools[t.Tool], crons[cron] = true, true
	}
	return nil
}

// checkCell validates t's cell trigger, the first of the manifest's unless
// *seen already says there was one.
func (m Manifest) checkCell(i int, t Trigger, seen *bool) error {
	if len(t.Cell) == 0 {
		return nil
	}
	if *seen {
		return fmt.Errorf("trigger %d: a module has one cell trigger; list every cell in it", i)
	}
	*seen = true
	return m.checkListens(i, t.Cell)
}

// checkHTTP validates t's http trigger, the first of the manifest's unless
// *seen already says there was one.
func checkHTTP(i int, t Trigger, seen *bool) error {
	if t.HTTP == nil {
		return nil
	}
	if *seen {
		return fmt.Errorf("trigger %d: a module has one http trigger; list every route in it", i)
	}
	*seen = true
	if err := t.HTTP.check(); err != nil {
		return fmt.Errorf("trigger %d: %w", i, err)
	}
	return nil
}

// kinds counts how many of cron, tool, cell and http the trigger sets.
func (t Trigger) kinds() int {
	n := 0
	for _, set := range []bool{t.Cron != "", t.Tool != "", len(t.Cell) > 0, t.HTTP != nil} {
		if set {
			n++
		}
	}
	return n
}

// checkListens refuses a listened cell that the module may not read or that
// it owns itself: a module reacting to its own cell would run itself.
func (m Manifest) checkListens(i int, ids []string) error {
	short := m.Name[strings.LastIndex(m.Name, "/")+1:] // local/x names cells x.…
	for _, id := range ids {
		switch {
		case !cellIDPattern.MatchString(id):
			return fmt.Errorf("trigger %d: cell %q is not a full cell id like module.cell", i, id)
		case !slices.Contains(m.Cells.Read, id):
			return fmt.Errorf("trigger %d: cell %q is not in cells.read", i, id)
		case strings.HasPrefix(id, short+"."):
			return fmt.Errorf("trigger %d: a module cannot listen to its own cell %q", i, id)
		}
	}
	return nil
}

// Limits of an http trigger's routes.
const (
	MaxRoutes    = 32  // routes in one trigger
	MaxRoutePath = 128 // bytes of one path
)

// routeMethods are the methods a route may answer. HEAD and OPTIONS are the
// node's: a browser's preflight is never the module's to answer (R-HTTP-3).
var routeMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

// routePathPattern is a route's path: absolute, plain characters, no query.
var routePathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)

// check validates the routes: at least one, each path plain and without a
// dot segment or an empty one, each method one the node allows, and no
// path and method twice.
func (h HTTP) check() error {
	if len(h.Routes) == 0 || len(h.Routes) > MaxRoutes {
		return fmt.Errorf("http: 1..%d routes", MaxRoutes)
	}
	seen := map[string]bool{}
	for _, r := range h.Routes {
		if err := r.checkPath(); err != nil {
			return err
		}
		if len(r.Methods) == 0 {
			return fmt.Errorf("http: route %q needs methods", r.Path)
		}
		for _, m := range r.Methods {
			if !slices.Contains(routeMethods, m) {
				return fmt.Errorf("http: route %q: method %q is not one of %s", r.Path, m, strings.Join(routeMethods, ", "))
			}
			if seen[m+" "+r.Path] {
				return fmt.Errorf("http: route %s %s is declared twice", m, r.Path)
			}
			seen[m+" "+r.Path] = true
		}
	}
	return nil
}

// checkPath refuses a path that is not plain, or has a dot segment or an
// empty one before its end ("/a/" is a path of its own).
func (r Route) checkPath() error {
	if len(r.Path) > MaxRoutePath || !routePathPattern.MatchString(r.Path) {
		return fmt.Errorf("http: route path %q: an absolute path of letters, digits and . _ ~ - /, at most %d bytes", r.Path, MaxRoutePath)
	}
	segs := strings.Split(r.Path[1:], "/")
	for i, seg := range segs {
		if seg == "." || seg == ".." || (seg == "" && i < len(segs)-1) {
			return fmt.Errorf("http: route path %q has an empty or dot segment", r.Path)
		}
	}
	return nil
}
