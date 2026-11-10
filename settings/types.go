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

// SecretRef is a secret by reference: "env:NAME" reads NAME or the file
// named by NAME_FILE. A literal secret is refused when the file is read.
type SecretRef string

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// UnmarshalYAML refuses anything but env:NAME; the error never repeats the value.
func (r *SecretRef) UnmarshalYAML(n *yaml.Node) error {
	name, ok := strings.CutPrefix(n.Value, "env:")
	if n.Kind != yaml.ScalarNode || !ok || !envName.MatchString(name) {
		return errors.New("must be env:NAME, the variable that holds the secret; a secret is never written in the file")
	}
	*r = SecretRef(n.Value)
	return nil
}

// Name is the variable's name.
func (r SecretRef) Name() string { return strings.TrimPrefix(string(r), "env:") }

// Resolve reads the secret; "" when the variable is unset.
func (r SecretRef) Resolve(src secrets.Source) (string, error) {
	if r == "" {
		return "", nil
	}
	return src.Secret(r.Name())
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
