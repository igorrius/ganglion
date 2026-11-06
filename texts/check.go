package texts

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/redact"
)

// Things a translation may hold only if its English source does (R-LANG-5).
var (
	// addressPatterns are what a chat app may turn into a link or a command to tap: a scheme, a bare
	// domain (a mail address holds one), a command at the start of a word.
	addressPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://`),
		regexp.MustCompile(`(?i)\bwww\.`),
		regexp.MustCompile(`(?i)\b[a-z0-9-]+\.[a-z]{2,}\b`),
		regexp.MustCompile(`(?:^|\s)/[a-zA-Z]\w*`),
	}
	mentionPattern = regexp.MustCompile(`(^|[^\p{L}\p{N}])@\p{L}`)
	// valueThenSeparator is a value followed by ":" or "=", which the redaction reads as a secret's
	// name and its value: a question would be refused, or show less than what runs.
	valueThenSeparator = regexp.MustCompile(`\}\s*[:=]`)
)

// markupMarks are the characters Markdown formats with.
const markupMarks = "*_`~[]<>#|"

// allowedFormat are the invisible characters real languages need: joiners for Persian and Indic
// scripts, and direction marks and isolates for right-to-left text. Others, such as an override, can
// make a text read other than it is.
const allowedFormat = "\u200c\u200d\u200e\u200f\u061c\u2066\u2067\u2068\u2069"

// probes are values a text is tried with, to see whether its wording makes the redaction hide one.
var probes = []string{"probe", "api_key"}

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
	case !hasWord(s):
		return "empty"
	case utf8.RuneCountInString(s) > limit(k):
		return "over the limit"
	case !slices.Equal(placeholders(s), placeholders(source)):
		return "placeholders differ"
	case hasHiddenCharacter(s):
		return "holds a hidden character"
	case addsAddress(source, s):
		return "adds an address"
	case mentionPattern.MatchString(s) && !mentionPattern.MatchString(source):
		return "adds a mention"
	case strings.Count(s, "\n") > strings.Count(source, "\n"):
		return "adds a line break"
	case valueThenSeparator.MatchString(s) && !valueThenSeparator.MatchString(source):
		return "puts a separator after a value"
	case hidesAValue(s) && !hidesAValue(source):
		return "makes the redaction hide a value"
	}
	for _, mark := range markupMarks {
		if strings.ContainsRune(s, mark) && !strings.ContainsRune(source, mark) {
			return "adds markup"
		}
	}
	return ""
}

// hasWord reports whether s has a letter or a digit outside its placeholders.
func hasWord(s string) bool {
	return strings.IndexFunc(placeholder.ReplaceAllString(s, ""), func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) >= 0
}

// hasHiddenCharacter finds a control or format character, or a line or paragraph separator, that no
// real language needs. A plain line break is counted separately.
func hasHiddenCharacter(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		if r == '\n' || strings.ContainsRune(allowedFormat, r) {
			return false
		}
		return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
	}) >= 0
}

// addsAddress reports an address in s that source does not hold.
func addsAddress(source, s string) bool {
	for _, p := range addressPatterns {
		known := p.FindAllString(strings.ToLower(source), -1)
		for _, m := range p.FindAllString(strings.ToLower(s), -1) {
			if !slices.Contains(known, m) {
				return true
			}
		}
	}
	return false
}

// hidesAValue reports whether the redaction would change s once its values are in: the person would
// approve less than they see, or the question would be refused.
func hidesAValue(s string) bool {
	for _, probe := range probes {
		filled := placeholder.ReplaceAllString(s, probe)
		if redact.String(filled) != filled {
			return true
		}
	}
	return false
}
