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

// Defang removes the scheme of every address Find would see in text,
// again until none is left (a removal can join two halves into a new
// one), so the text names no address.
func Defang(text string) string {
	for pattern.MatchString(text) {
		text = pattern.ReplaceAllStringFunc(text, func(u string) string {
			_, rest, _ := strings.Cut(u, "://")
			return rest
		})
	}
	return text
}

// Exact reports whether Find reads text as exactly one address, text
// itself.
func Exact(text string) bool {
	found := Find(text)
	return len(found) == 1 && found[0] == text
}
