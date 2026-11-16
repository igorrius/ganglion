package settings

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Change sets the field at Path (dotted, "agent.limits.daily_turns") to
// Value, as JSON decoding gives it: a string, a float64, a bool, a
// map[string]any or a []any. A nil Value removes the key, so the field
// takes its default.
type Change struct {
	Path  string
	Value any
}

// SetPath writes the changes into the file at path. It edits the YAML
// node tree, so comments and the order of everything else stay, and
// refuses, leaving the file as it was, when the result fails the checks
// the start runs (R-CFG-15). It does not consult the allowlist: that is
// the caller's (config.propose).
func SetPath(path string, cs []Change) error {
	return editDoc(path, func(doc *yaml.Node) error { return setPaths(doc, cs) })
}

// Apply is SetPath on the file's bytes: the edited file, checked.
func Apply(src []byte, cs []Change) ([]byte, error) {
	return editSrc(src, func(doc *yaml.Node) error { return setPaths(doc, cs) })
}

// setPaths applies the changes in order.
func setPaths(doc *yaml.Node, cs []Change) error {
	root, err := rootOf(doc)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if err := setPath(root, strings.Split(c.Path, "."), c.Value); err != nil {
			return fmt.Errorf("%s: %s: %w", FileName, c.Path, err)
		}
	}
	return nil
}

// setPath sets the value at keys under the mapping m, creating the
// mappings on the way; a nil value removes the key (removeAt).
func setPath(m *yaml.Node, keys []string, value any) error {
	if value == nil {
		return removeAt(m, keys)
	}
	if len(keys) > 1 {
		next, err := child(m, keys[0], yaml.MappingNode, tagMap)
		if err != nil {
			return errShape
		}
		return setPath(next, keys[1:], value)
	}
	var n yaml.Node
	if err := n.Encode(whole(value)); err != nil {
		return err
	}
	old, _ := field(m, keys[0])
	if old == nil {
		m.Content = append(m.Content, plain(keys[0]), &n)
		return nil
	}
	n.HeadComment, n.LineComment, n.FootComment = old.HeadComment, old.LineComment, old.FootComment
	*old = n
	return nil
}

// removeAt removes the key at keys under m and every mapping on the way
// that it leaves empty; a key that is not there is no error.
func removeAt(m *yaml.Node, keys []string) error {
	next, at := field(m, keys[0])
	switch {
	case next == nil:
		return nil
	case len(keys) == 1:
	case next.Kind != yaml.MappingNode:
		return errShape
	default:
		if err := removeAt(next, keys[1:]); err != nil {
			return err
		}
		if len(next.Content) > 0 {
			return nil
		}
	}
	m.Content = slices.Delete(m.Content, at, at+2)
	return nil
}

// errShape is a key on the way that holds no mapping.
var errShape = fmt.Errorf("a key on the way is not a section")

// whole turns the float64s JSON gives for whole numbers into ints, so 300
// is written as 300 and not 300.0, through maps and lists.
func whole(v any) any {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x)
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = whole(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = whole(e)
		}
		return out
	default:
		return v
	}
}

// Values are the values at paths in the file src, decoded from YAML. A
// path that is not in the file is not in the map: the field has its
// default.
func Values(src []byte, paths []string) (map[string]any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s", FileName, syntaxReason(err))
	}
	out := make(map[string]any, len(paths))
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return out, nil
	}
	for _, p := range paths {
		n := doc.Content[0]
		for key := range strings.SplitSeq(p, ".") {
			if n.Kind != yaml.MappingNode {
				n = nil
				break
			}
			if n, _ = field(n, key); n == nil {
				break
			}
		}
		if n == nil {
			continue
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", FileName, p, err)
		}
		out[p] = v
	}
	return out, nil
}
