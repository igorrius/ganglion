// Package text holds small string helpers shared by several packages.
package text

// Truncate keeps the first n runes of s and adds "…" when it cut
// anything.
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
