package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Shape types.
const (
	typeNumber  = "number"
	typeInteger = "integer"
	typeBoolean = "boolean"
	typeString  = "string"
	typeObject  = "object"
	typeList    = "list"
	typeEnum    = "enum"
	typeNull    = "null"
)

// Shape is a contract for a JSON value: a manifest's tool output and a
// cell's type. Nothing outside this language is accepted.
type Shape struct {
	Type     string // "number" | "integer" | "boolean" | "string" | "object" | "list" | "enum"
	Nullable bool
	Enum     []string
	Fields   map[string]Shape // object
	Optional map[string]bool  // object fields that may be missing
	Item     *Shape           // list
}

// UnmarshalYAML reads a shape: a scalar type name (a trailing "?" allows
// null), a list of strings (an enum), {list: <shape>} (an array) or a
// mapping of fields (an object; a field name ending in "?" may be missing).
func (s *Shape) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		return s.fromScalar(n)
	case yaml.SequenceNode:
		return s.fromEnum(n)
	case yaml.MappingNode:
		if len(n.Content) == 2 && n.Content[0].Value == typeList {
			item := new(Shape)
			if err := n.Content[1].Decode(item); err != nil {
				return err
			}
			*s = Shape{Type: typeList, Item: item}
			return nil
		}
		return s.fromObject(n)
	case yaml.DocumentNode, yaml.AliasNode:
	}
	return fmt.Errorf("line %d: a shape is a type name, a list of strings or a mapping", n.Line)
}

func (s *Shape) fromScalar(n *yaml.Node) error {
	name, nullable := strings.CutSuffix(n.Value, "?")
	switch name {
	case typeNumber, typeInteger, typeBoolean, typeString:
		*s = Shape{Type: name, Nullable: nullable}
		return nil
	}
	return fmt.Errorf("line %d: unknown type %q (number, integer, boolean, string)", n.Line, n.Value)
}

func (s *Shape) fromEnum(n *yaml.Node) error {
	if len(n.Content) == 0 {
		return fmt.Errorf("line %d: an enum needs at least one value", n.Line)
	}
	vals := make([]string, 0, len(n.Content))
	for _, e := range n.Content {
		if e.Kind != yaml.ScalarNode || e.Tag != "!!str" {
			return fmt.Errorf("line %d: an enum is a list of strings", e.Line)
		}
		vals = append(vals, e.Value)
	}
	*s = Shape{Type: typeEnum, Enum: vals}
	return nil
}

func (s *Shape) fromObject(n *yaml.Node) error {
	obj := Shape{Type: typeObject, Fields: map[string]Shape{}, Optional: map[string]bool{}}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, optional := strings.CutSuffix(n.Content[i].Value, "?")
		if _, dup := obj.Fields[name]; dup {
			return fmt.Errorf("line %d: field %q is declared twice", n.Content[i].Line, name)
		}
		var f Shape
		if err := n.Content[i+1].Decode(&f); err != nil {
			return err
		}
		obj.Fields[name] = f
		if optional {
			obj.Optional[name] = true
		}
	}
	*s = obj
	return nil
}

// Check reports the first way v does not fit the shape. Its message names
// fields and types, never a value: v is module output (R-RUN-14). A field
// declared nullable may also be missing.
func (s Shape) Check(v json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return errors.New("not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("not valid JSON")
	}
	return s.check(x, "")
}

func (s Shape) check(x any, path string) error {
	if x == nil && s.Nullable {
		return nil
	}
	switch s.Type {
	case typeNumber, typeBoolean, typeString:
		if jsonType(x) != s.Type {
			return mismatch(path, s.Type, x)
		}
	case typeInteger:
		num, ok := x.(json.Number)
		if !ok || strings.ContainsAny(num.String(), ".eE") {
			return mismatch(path, s.Type, x)
		}
	case typeEnum:
		str, ok := x.(string)
		if !ok {
			return mismatch(path, typeString, x)
		}
		if !slices.Contains(s.Enum, str) {
			return at(path, "not one of the declared values")
		}
	case typeList:
		return s.checkList(x, path)
	case typeObject:
		return s.checkObject(x, path)
	}
	return nil
}

func (s Shape) checkList(x any, path string) error {
	list, ok := x.([]any)
	if !ok {
		return mismatch(path, typeList, x)
	}
	for i, e := range list {
		if err := s.Item.check(e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func (s Shape) checkObject(x any, path string) error {
	obj, ok := x.(map[string]any)
	if !ok {
		return mismatch(path, typeObject, x)
	}
	for _, name := range sortedKeys(s.Fields) {
		f := s.Fields[name]
		v, present := obj[name]
		if !present {
			if s.Optional[name] || f.Nullable {
				continue
			}
			return at(join(path, name), "missing")
		}
		if err := f.check(v, join(path, name)); err != nil {
			return err
		}
	}
	for _, name := range sortedKeys(obj) {
		if _, declared := s.Fields[name]; !declared {
			return at(join(path, name), "not declared")
		}
	}
	return nil
}

// jsonType names the JSON type of a decoded value.
func jsonType(x any) string {
	switch x.(type) {
	case nil:
		return typeNull
	case bool:
		return typeBoolean
	case json.Number:
		return typeNumber
	case string:
		return typeString
	case []any:
		return typeList
	}
	return typeObject
}

func mismatch(path, want string, got any) error {
	return at(path, fmt.Sprintf("expected %s, got %s", want, jsonType(got)))
}

// at prefixes msg with the field path; the top-level value has none.
func at(path, msg string) error {
	if path == "" {
		return errors.New(msg)
	}
	return fmt.Errorf("field %s: %s", path, msg)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HasString reports whether v holds a string anywhere: such a value taints
// the session (R-RUN-13). A value that is not JSON counts as text.
func HasString(v json.RawMessage) bool {
	var x any
	if err := json.Unmarshal(v, &x); err != nil {
		return true
	}
	return hasString(x)
}

func hasString(x any) bool {
	switch t := x.(type) {
	case string:
		return true
	case []any:
		return slices.ContainsFunc(t, hasString)
	case map[string]any:
		for _, e := range t {
			if hasString(e) {
				return true
			}
		}
	}
	return false
}
