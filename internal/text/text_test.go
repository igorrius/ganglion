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

func TestCodeSpanFencesPastEveryBacktick(t *testing.T) {
	for s, want := range map[string]string{
		"ls -l":  "`ls -l`",
		"a`b":    "`` a`b ``",
		"``x`":   "``` ``x` ```",
		"`a` b`": "`` `a` b` ``",
	} {
		if got := CodeSpan(s); got != want {
			t.Errorf("CodeSpan(%q) = %q, want %q", s, got, want)
		}
	}
}
