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

// Telegram reads "/word" after a space or a mark as a bot command and makes
// it a link; only code is left alone.
func TestCodeSlashWordsKeepsPathsFromBecomingCommands(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "",
		"clean /tmp/ganglion":         "clean `/tmp/ganglion`",
		"/tmp/ganglion is full.":      "`/tmp/ganglion` is full.",
		"sizes (/var/log, ~/src)":     "sizes (`/var/log`, `~/src`)",
		"keep a/b and ~/.cache as is": "keep a/b and ~/.cache as is",
		"already `/tmp/x` in code":    "already `/tmp/x` in code",
		"`du -sh /tmp` then /srv":     "`du -sh /tmp` then `/srv`",
		"**/etc** and /opt\n/home/o":  "**`/etc`** and `/opt`\n`/home/o`",
		"50/50 and 1/2":               "50/50 and 1/2",
		"a lone / slash":              "a lone / slash",
		"файл/x and /файл":            "файл/x and /файл",
	} {
		if got := CodeSlashWords(in); got != want {
			t.Errorf("CodeSlashWords(%q) = %q, want %q", in, got, want)
		}
	}
}
