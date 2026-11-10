// Package schemas holds the JSON Schemas of ganglion's YAML files
// (ADR-0023). The Go loaders are the validators; the schemas serve
// editors, documentation and the agent, which gets them as tool input
// schemas when it proposes configuration changes.
package schemas

import (
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

//go:embed *.schema.json
var files embed.FS

// Get returns a schema by file name, e.g. "node.schema.json".
func Get(name string) ([]byte, error) { return files.ReadFile(name) }

// Object describes one object of a schema: its property names, required
// names, whether other properties are refused, and each property's enum.
type Object struct {
	Properties           []string
	Required             []string
	AdditionalProperties bool
	Enums                map[string][]string
}

// At returns the object at a JSON pointer such as "" (the root),
// "/$defs/model" or "/properties/triggers/items".
func At(schema []byte, pointer string) (Object, error) {
	var node any
	if err := json.Unmarshal(schema, &node); err != nil {
		return Object{}, err
	}
	for _, part := range splitPointer(pointer) {
		m, ok := node.(map[string]any)
		if !ok {
			return Object{}, fmt.Errorf("schemas: %s: %q is not an object", pointer, part)
		}
		if node, ok = m[part]; !ok {
			return Object{}, fmt.Errorf("schemas: %s: no %q", pointer, part)
		}
	}
	m, ok := node.(map[string]any)
	if !ok {
		return Object{}, fmt.Errorf("schemas: %s is not an object", pointer)
	}
	o := Object{AdditionalProperties: true, Enums: map[string][]string{}}
	if ap, ok := m["additionalProperties"].(bool); ok {
		o.AdditionalProperties = ap
	}
	props, _ := m["properties"].(map[string]any)
	for name, p := range props {
		o.Properties = append(o.Properties, name)
		if pm, ok := p.(map[string]any); ok {
			if enum, ok := pm["enum"].([]any); ok {
				for _, v := range enum {
					o.Enums[name] = append(o.Enums[name], fmt.Sprint(v))
				}
				slices.Sort(o.Enums[name])
			}
		}
	}
	slices.Sort(o.Properties)
	req, _ := m["required"].([]any)
	for _, r := range req {
		o.Required = append(o.Required, fmt.Sprint(r))
	}
	slices.Sort(o.Required)
	return o, nil
}

func splitPointer(p string) []string {
	if p == "" {
		return nil
	}
	var out []string
	cur := ""
	for _, r := range p[1:] {
		if r == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

// YAMLFields returns the YAML keys of struct v's fields, sorted: the tag
// name, or the lower-cased field name when untagged; "-" fields are left
// out and ",inline" structs are flattened.
func YAMLFields(v any) []string {
	var out []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for f := range t.Fields() {
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			switch {
			case name == "-":
				continue
			case strings.Contains(opts, "inline"):
				walk(f.Type)
				continue
			case name == "":
				name = strings.ToLower(f.Name)
			}
			out = append(out, name)
		}
	}
	walk(reflect.TypeOf(v))
	slices.Sort(out)
	return out
}
