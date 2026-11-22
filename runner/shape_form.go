package runner

import (
	"maps"
	"slices"
	"strings"
)

// Equal reports whether two shapes accept the same values: the same type and
// nullability, the same fields with the same optional set, the same enum
// values (as a set) and the same list item. A link joins two ports only when
// their shapes are equal (R-BUS-5).
func (s Shape) Equal(o Shape) bool {
	if s.Type != o.Type || s.Nullable != o.Nullable {
		return false
	}
	switch s.Type {
	case typeEnum:
		return sameSet(s.Enum, o.Enum)
	case typeList:
		return s.Item != nil && o.Item != nil && s.Item.Equal(*o.Item)
	case typeObject:
		return s.equalFields(o)
	}
	return true
}

func (s Shape) equalFields(o Shape) bool {
	if len(s.Fields) != len(o.Fields) {
		return false
	}
	for name, f := range s.Fields {
		g, ok := o.Fields[name]
		if !ok || !f.Equal(g) || s.Optional[name] != o.Optional[name] {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	x := slices.Sorted(maps.Keys(setOf(a)))
	y := slices.Sorted(maps.Keys(setOf(b)))
	return slices.Equal(x, y)
}

func setOf(v []string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, e := range v {
		m[e] = true
	}
	return m
}

// String is a short one-line form of the shape for a card: `number`,
// `string?` (nullable), `a|b` (enum), `[number]` (list) and
// `{load: number, soc?: number}` (object, fields sorted, `?` after an
// optional field's name).
func (s Shape) String() string {
	var b strings.Builder
	s.write(&b)
	return b.String()
}

func (s Shape) write(b *strings.Builder) {
	switch s.Type {
	case typeEnum:
		b.WriteString(strings.Join(slices.Sorted(maps.Keys(setOf(s.Enum))), "|"))
	case typeList:
		b.WriteString("[")
		if s.Item != nil {
			s.Item.write(b)
		}
		b.WriteString("]")
	case typeObject:
		b.WriteString("{")
		for i, name := range sortedKeys(s.Fields) {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(name)
			if s.Optional[name] {
				b.WriteString("?")
			}
			b.WriteString(": ")
			s.Fields[name].write(b)
		}
		b.WriteString("}")
	default:
		b.WriteString(s.Type)
	}
	if s.Nullable {
		b.WriteString("?")
	}
}
