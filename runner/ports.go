package runner

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
)

// NotifyOutput is the name of the implicit output of a module that may notify
// the owner (R-BUS-5). No declared port takes it.
const NotifyOutput = "notify"

// portNamePattern is a port name: a NATS subject token and a field name.
var portNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Ports are the typed ends a module offers to links (R-BUS-5): the events it
// publishes and takes, the calls it answers and makes. Each maps a port name
// to the shape of what crosses it.
type Ports struct {
	Emits   map[string]Shape    `yaml:"emits,omitempty"`   // events it publishes
	Accepts map[string]Shape    `yaml:"accepts,omitempty"` // events it takes
	Serves  map[string]PortCall `yaml:"serves,omitempty"`  // calls it answers
	Calls   map[string]PortCall `yaml:"calls,omitempty"`   // calls it makes
}

// PortCall is a request and its answer: the shapes of a served or a called
// port. Both are required.
type PortCall struct {
	In  Shape `yaml:"in"`
	Out Shape `yaml:"out"`
}

// Any reports whether the module declares at least one port.
func (p Ports) Any() bool {
	return len(p.Emits)+len(p.Accepts)+len(p.Serves)+len(p.Calls) > 0
}

// portKind is one of the four maps of Ports, for the checks that treat them
// alike: an event kind has shapes, a call kind has calls.
type portKind struct {
	name  string
	shape map[string]Shape
	call  map[string]PortCall
}

// kinds lists the four kinds in a fixed order.
func (p Ports) kinds() []portKind {
	return []portKind{{"emits", p.Emits, nil}, {"accepts", p.Accepts, nil}, {"serves", nil, p.Serves}, {"calls", nil, p.Calls}}
}

// check validates the names (the pattern, not reserved, unique across all
// four kinds) and that every shape is given.
func (p Ports) check() error {
	seen := map[string]string{}
	for _, k := range p.kinds() {
		for _, name := range k.names() {
			switch {
			case !portNamePattern.MatchString(name):
				return fmt.Errorf("ports.%s %q: a name is a lower-case letter, then letters, digits and _, at most 32", k.name, name)
			case name == NotifyOutput:
				return fmt.Errorf("ports.%s %q: the name is reserved for the notice of a module with notify", k.name, name)
			case seen[name] != "":
				return fmt.Errorf("ports.%s %q: the name is already a port in ports.%s; a link names a port by its name alone", k.name, name, seen[name])
			}
			seen[name] = k.name
			if s, ok := k.shape[name]; ok && s.Type == "" {
				return fmt.Errorf("ports.%s %q: a shape is required", k.name, name)
			}
			if c, ok := k.call[name]; ok && (c.In.Type == "" || c.Out.Type == "") {
				return fmt.Errorf("ports.%s %q: in and out are both required", k.name, name)
			}
		}
	}
	return nil
}

// names are the port names of the kind, sorted, so the first refusal is the
// same on every run.
func (k portKind) names() []string {
	var names []string
	for n := range k.shape {
		names = append(names, n)
	}
	for n := range k.call {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Names are the sorted port names of each kind.
func (p Ports) Names() (emits, accepts, serves, calls []string) {
	k := p.kinds()
	return k[0].names(), k[1].names(), k[2].names(), k[3].names()
}

// notifyShape is the payload of the implicit notify output: the notice's
// text and its time (RFC 3339, UTC).
func notifyShape() Shape {
	return Shape{Type: typeObject, Fields: map[string]Shape{
		"text": {Type: typeString},
		"at":   {Type: typeString},
	}}
}

// Emits are the outputs of the module by name: the declared ones, and
// `notify` with a fixed shape when the module may notify the owner.
func (m Manifest) Emits() map[string]Shape {
	out := make(map[string]Shape, len(m.Ports.Emits)+1)
	maps.Copy(out, m.Ports.Emits)
	if m.Capabilities.Notify != nil {
		out[NotifyOutput] = notifyShape()
	}
	return out
}
