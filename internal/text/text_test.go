package text

import "testing"

func TestTruncateCountsRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
		{"привет", 3, "при…"},
		{"привет", 6, "привет"},
	} {
		if got := Truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
