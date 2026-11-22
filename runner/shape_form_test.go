package runner

import "testing"

func mustShape(t *testing.T, src string) Shape {
	t.Helper()
	m, err := ParseManifest([]byte("name: x\nversion: 1\nports:\n  emits:\n    p: " + src + "\n"))
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return m.Ports.Emits["p"]
}

func TestShapeEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"same scalar", "number", "number", true},
		{"other scalar", "number", "integer", false},
		{"nullability", "number", "number?", false},
		{"object field order", "{a: number, b: string}", "{b: string, a: number}", true},
		{"object extra field", "{a: number}", "{a: number, b: string}", false},
		{"object field type", "{a: number}", "{a: string}", false},
		{"optional set", `{"a?": number}`, "{a: number}", false},
		{"enum as a set", "[x, y]", "[y, x]", true},
		{"enum differs", "[x, y]", "[x, z]", false},
		{"list item", "{list: number}", "{list: number}", true},
		{"list item differs", "{list: number}", "{list: string}", false},
		{"nested", "{p: {list: {a: number}}}", "{p: {list: {a: number}}}", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustShape(t, tc.a).Equal(mustShape(t, tc.b)); got != tc.want {
				t.Fatalf("Equal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShapeString(t *testing.T) {
	tests := map[string]string{
		"number":                       "number",
		`"string?"`:                    "string?",
		"{soc: number, load: number}":  "{load: number, soc: number}",
		`{"a?": number}`:               "{a?: number}",
		"[y, x]":                       "x|y",
		"{list: number}":               "[number]",
		`{p: {list: {a: "boolean?"}}}`: "{p: [{a: boolean?}]}",
	}
	for src, want := range tests {
		if got := mustShape(t, src).String(); got != want {
			t.Errorf("String(%s) = %q, want %q", src, got, want)
		}
	}
}
