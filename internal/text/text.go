// Package text holds small string helpers shared by several packages.
package text

import "strings"

// Truncate keeps the first n runes of s and adds "…" when it cut
// anything.
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// CodeSpan is s as one Markdown code span that shows s exactly: fenced by
// more backticks than the longest run in s, so a backtick inside cannot end
// the span early and let what follows render as Markdown (a link would hide
// its target). The spaces inside a longer fence are the ones Markdown drops.
func CodeSpan(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	if longest == 0 {
		return "`" + s + "`"
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + s + " " + fence
}
