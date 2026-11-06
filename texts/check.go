package texts

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Things a translation may hold only if its English source does (R-LANG-5).
var (
	addressPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://|\bwww\.`)
	mentionPattern = regexp.MustCompile(`(^|[^\p{L}\p{N}])@\p{L}`)
)

// markupMarks are the characters Markdown formats with.
const markupMarks = "*_`~[]<>#|"

// bidiOverrides reorder what a person sees, so a text can read other than it is.
const bidiOverrides = "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069"

// placeholders returns the placeholders of s, sorted.
func placeholders(s string) []string {
	return slices.Sorted(slices.Values(placeholder.FindAllString(s, -1)))
}

// Check splits tr, a translation of en, into the texts it accepts and those it rejects, with a
// reason for each. It applies to anything not written by this repository: a machine translation
// and an operator's file. It cannot see meaning.
func Check(en, tr map[Key]string) (ok, rejected map[Key]string) {
	ok, rejected = map[Key]string{}, map[Key]string{}
	for k, s := range tr {
		source, known := en[k]
		if !known {
			rejected[k] = "unknown key"
			continue
		}
		if reason := checkText(k, source, s); reason != "" {
			rejected[k] = reason
			continue
		}
		ok[k] = s
	}
	return ok, rejected
}

// checkText returns why s cannot stand for source, or "".
func checkText(k Key, source, s string) string {
	switch {
	case strings.TrimSpace(s) == "":
		return "empty"
	case utf8.RuneCountInString(s) > limit(k):
		return "over the limit"
	case !slices.Equal(placeholders(s), placeholders(source)):
		return "placeholders differ"
	case addressPattern.MatchString(s) && !addressPattern.MatchString(source):
		return "adds an address"
	case mentionPattern.MatchString(s) && !mentionPattern.MatchString(source):
		return "adds a mention"
	case strings.ContainsAny(s, bidiOverrides):
		return "holds a direction override"
	case strings.Count(s, "\n") > strings.Count(source, "\n"):
		return "adds a line break"
	}
	for _, mark := range markupMarks {
		if strings.ContainsRune(s, mark) && !strings.ContainsRune(source, mark) {
			return "adds markup"
		}
	}
	return ""
}
