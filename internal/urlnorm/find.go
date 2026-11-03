package urlnorm

import (
	"regexp"
	"strings"
)

// pattern finds an http(s) address in text: it runs up to whitespace or a
// closing ")", ">" or quote. There is no word boundary before the
// scheme, so "xhttps://a" holds an address too. It is the only copy:
// what counts as an address in the owner's words and in search results
// (the agent's known URLs) and what web.search defangs are the same
// thing (ruling R19).
var pattern = regexp.MustCompile(`(?i)https?://[^\s)>"]+`)

// Find returns the addresses in text; punctuation that ends a sentence
// after an address is not part of it.
func Find(text string) []string {
	found := pattern.FindAllString(text, -1)
	for i, u := range found {
		found[i] = strings.TrimRight(u, ".,;:!?")
	}
	return found
}

// Defang removes the scheme of every address Find would see in text, in
// one pass: at each "://" it looks at what it has written so far, and
// when that ends in "http" or "https" (any case) it drops both. A drop
// can join two halves into a new scheme ("ht" + "tps"), but that one is
// seen at the next "://", so nothing is left for Find, and nested
// schemes cost no extra pass.
func Defang(text string) string {
	out := make([]byte, 0, len(text))
	for {
		i := strings.Index(text, "://")
		if i < 0 {
			return string(append(out, text...))
		}
		out = append(out, text[:i]...)
		text = text[i+3:]
		if n := schemeSuffix(out); n > 0 {
			out = out[:len(out)-n]
			continue
		}
		out = append(out, "://"...)
	}
}

// schemeSuffix is the length of "https" or "http" at the end of s, any
// case, or 0.
func schemeSuffix(s []byte) int {
	for _, scheme := range []string{"https", "http"} {
		if len(s) >= len(scheme) && strings.EqualFold(string(s[len(s)-len(scheme):]), scheme) {
			return len(scheme)
		}
	}
	return 0
}

// Exact reports whether Find reads text as exactly one address, text
// itself.
func Exact(text string) bool {
	found := Find(text)
	return len(found) == 1 && found[0] == text
}
