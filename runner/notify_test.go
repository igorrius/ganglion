package runner

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCutText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"999 characters stay", strings.Repeat("a", 999), strings.Repeat("a", 999)},
		{"1000 characters stay", strings.Repeat("a", 1000), strings.Repeat("a", 1000)},
		{"1001 characters are cut with an ellipsis", strings.Repeat("a", 1001), strings.Repeat("a", 999) + "…"},
		{"multi-byte characters count once", strings.Repeat("я", 1000), strings.Repeat("я", 1000)},
		{"multi-byte cut", strings.Repeat("я", 1500), strings.Repeat("я", 999) + "…"},
		{"invalid UTF-8 is replaced", "a\xffb", "a�b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cutText(tc.in)
			if got != tc.want {
				t.Fatalf("cutText = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("cutText result is not valid UTF-8: %q", got)
			}
		})
	}
}
