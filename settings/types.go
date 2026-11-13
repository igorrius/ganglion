package settings

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/internal/size"
	"github.com/igorrius/ganglion/secrets"
)

// off is the word that turns a setting off.
const off = "off"

// auto is the value that leaves a choice to the node.
const auto = "auto"

// SecretRef is a secret by reference: "env:NAME" reads the variable NAME
// or the file named by NAME_FILE; "store:NAME" reads NAME in the node
// scope of the secret store (ADR-0077). A literal secret is refused when
// the file is read.
type SecretRef string

// Scheme is where a secret reference reads from.
type Scheme string

// The schemes.
const (
	SchemeEnv   Scheme = "env"   // the variable NAME or the file NAME_FILE
	SchemeStore Scheme = "store" // the node scope of the secret store
)

// Sources are where references are read, one per scheme.
type Sources struct{ Env, Store secrets.Source }

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// UnmarshalYAML refuses anything but env:NAME and store:NAME; the error
// never repeats the value.
func (r *SecretRef) UnmarshalYAML(n *yaml.Node) error {
	ref := SecretRef(n.Value)
	if n.Kind != yaml.ScalarNode || ref.Scheme() == "" || !envName.MatchString(ref.Name()) {
		return errors.New("must be env:NAME or store:NAME: the variable, or the stored secret, that holds the value; a secret is never written in the file")
	}
	*r = ref
	return nil
}

// Scheme is where the reference reads from; "" for an empty reference and
// for one of no known scheme.
func (r SecretRef) Scheme() Scheme {
	scheme, _, ok := strings.Cut(string(r), ":")
	if s := Scheme(scheme); ok && (s == SchemeEnv || s == SchemeStore) {
		return s
	}
	return ""
}

// Name is the variable's or the stored secret's name, without the scheme.
func (r SecretRef) Name() string {
	_, name, _ := strings.Cut(string(r), ":")
	return name
}

// Resolve reads the secret from the source of its scheme; "" when it is
// not set.
func (r SecretRef) Resolve(s Sources) (string, error) {
	switch r.Scheme() {
	case SchemeEnv:
		return s.Env.Secret(r.Name())
	case SchemeStore:
		return s.Store.Secret(r.Name())
	}
	return "", nil
}

// HowToSet says how the secret a reference names is set, for the message
// that says it is not.
func (r SecretRef) HowToSet() string {
	if r.Scheme() == SchemeStore {
		return "set it with: ganglion secret set " + r.Name()
	}
	return "set it or " + r.Name() + "_FILE"
}

// Duration is a Go duration or "off" (0).
type Duration time.Duration

// UnmarshalYAML reads "90s", "6h" or "off".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == off {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil || v < 0 {
		return errors.New(`want a duration such as "90s" or "6h", or off`)
	}
	*d = Duration(v)
	return nil
}

// Count is a whole number of 0 or more, or "off" (0).
type Count int

// UnmarshalYAML reads a whole number or "off".
func (c *Count) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == off {
		*c = 0
		return nil
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil || v < 0 {
		return errors.New("must be 0 or more, or off")
	}
	*c = Count(v)
	return nil
}

// Money is US dollars, 0 or more, or "off" (0).
type Money float64

// UnmarshalYAML reads a number or "off".
func (m *Money) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == off {
		*m = 0
		return nil
	}
	v, err := strconv.ParseFloat(n.Value, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("must be 0 or more US dollars, or off")
	}
	*m = Money(v)
	return nil
}

// Size is a byte size in size.Parse's form; 0 is unset.
type Size uint64

// UnmarshalYAML reads "100MiB" and the like.
func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := size.Parse(n.Value)
	if err != nil {
		return errors.New(`want a size such as "100MiB"`)
	}
	*s = Size(v)
	return nil
}

// UnmarshalYAML reads "off" (every limit off) or a mapping over the defaults.
func (l *Limits) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Value == off {
		*l = Limits{}
		return nil
	}
	type plain Limits
	p := plain(*l)
	if err := decodeStrict(n, &p); err != nil {
		return err
	}
	*l = Limits(p)
	return nil
}
