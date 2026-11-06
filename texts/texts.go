// Package texts holds every fixed text a person reads, under a stable key, in the languages the
// node knows (R-LANG-1, R-LANG-2, ADR-0053). It reads only its embedded files; who a person is and
// what their language is belong to package language.
package texts

import (
	"embed"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Lang is a language's primary subtag, from the table of native names.
type Lang string

// English is the default, and what a text falls back to.
const English Lang = "en"

// Key names one fixed text. Its prefix says where the text goes, and so its limit.
type Key string

// The keys. en.yaml holds their English texts; a test keeps the two the same.
const (
	ButtonApprove Key = "button.approve"
	ButtonDeny    Key = "button.deny"

	ToastApproved   Key = "toast.approved"
	ToastDeclined   Key = "toast.declined"
	ToastAccepted   Key = "toast.accepted"
	ToastNotYours   Key = "toast.not_yours"
	ToastNoButton   Key = "toast.no_button"
	ToastUsed       Key = "toast.used"
	ToastExpired    Key = "toast.expired"
	ToastNotAllowed Key = "toast.not_allowed"
)

var allKeys = []Key{
	ButtonApprove, ButtonDeny,
	ToastApproved, ToastDeclined, ToastAccepted, ToastNotYours, ToastNoButton, ToastUsed, ToastExpired, ToastNotAllowed,
}

// Keys returns every key.
func Keys() []Key { return allKeys }

// Limits of a text, in characters: Telegram's for a toast, one that fits a button on a phone, and
// the length of one message for the rest.
const (
	limitToast   = 200
	limitButton  = 32
	limitMessage = 4000
)

// limit returns the most characters k's text may have.
func limit(k Key) int {
	switch {
	case strings.HasPrefix(string(k), "toast."):
		return limitToast
	case strings.HasPrefix(string(k), "button."):
		return limitButton
	default:
		return limitMessage
	}
}

// IsApproval reports whether a person reads k's text to decide an approval. A machine translation
// missing any of these is refused whole (R-LANG-5).
func IsApproval(k Key) bool {
	return k == ButtonApprove || k == ButtonDeny || strings.HasPrefix(string(k), "ask.")
}

// Source says where a text came from.
type Source string

// Sources, highest priority first (R-LANG-4).
const (
	SourceOperator Source = "operator"
	SourceShipped  Source = "shipped"
	SourceMachine  Source = "machine"
	SourceEnglish  Source = "english"
)

// Vars are the values of a text's placeholders.
type Vars map[string]string

// PackFunc finds a machine translation. It must not block: it is called while a message is built.
type PackFunc func(Lang, Key) (string, bool)

//go:embed lang/*.yaml
var langFiles embed.FS

// shipped holds the embedded files. A file that does not parse fails at the first test, so a
// running binary always has them.
var shipped = mustLoad(English, "ru", "uk")

func mustLoad(langs ...Lang) map[Lang]map[Key]string {
	out := make(map[Lang]map[Key]string, len(langs))
	for _, l := range langs {
		raw, err := langFiles.ReadFile("lang/" + string(l) + ".yaml")
		if err != nil {
			panic(fmt.Sprintf("texts: %s: %v", l, err))
		}
		file := map[Key]string{}
		if err := yaml.Unmarshal(raw, &file); err != nil {
			panic(fmt.Sprintf("texts: %s: %v", l, err))
		}
		out[l] = file
	}
	return out
}

// EnglishText returns k's English text.
func EnglishText(k Key) string { return shipped[English][k] }

// Option configures a Catalog.
type Option func(*Catalog)

// WithFiles sets the operator's files, already checked. They come before everything else.
func WithFiles(files map[Lang]map[Key]string) Option {
	return func(c *Catalog) { c.operator = files }
}

// WithPacks sets where machine translations are found.
func WithPacks(p PackFunc) Option { return func(c *Catalog) { c.pack = p } }

// Catalog finds a text for a language, key by key.
type Catalog struct {
	operator map[Lang]map[Key]string
	pack     PackFunc
}

// New returns a catalogue of the shipped files and whatever opts add.
func New(opts ...Option) (*Catalog, error) {
	c := &Catalog{pack: func(Lang, Key) (string, bool) { return "", false }}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// find returns k's template in l and where it came from (R-LANG-4).
func (c *Catalog) find(l Lang, k Key) (string, Source) {
	if s, ok := c.operator[l][k]; ok {
		return s, SourceOperator
	}
	if s, ok := shipped[l][k]; ok {
		return s, SourceShipped
	}
	if s, ok := c.pack(l, k); ok {
		return s, SourceMachine
	}
	return EnglishText(k), SourceEnglish
}

// Source returns where k's text in l comes from.
func (c *Catalog) Source(l Lang, k Key) Source {
	_, src := c.find(l, k)
	return src
}

// Has reports whether l has every approval text from a file or a translation.
func (c *Catalog) Has(l Lang) bool {
	for _, k := range allKeys {
		if IsApproval(k) && c.Source(l, k) == SourceEnglish {
			return false
		}
	}
	return true
}

// placeholder matches anything in braces, so a translated placeholder is seen as one.
var placeholder = regexp.MustCompile(`\{[^{}]*\}`)

// Text returns k's text in l for a message, which the transport renders as Markdown. The template
// is escaped, so a translation cannot add formatting; the values keep theirs.
func (c *Catalog) Text(l Lang, k Key, v Vars) string {
	s, _ := c.find(l, k)
	return fill(s, v, escapeMarkdown)
}

// Label returns k's text in l for a button or a toast, which are not rendered.
func (c *Catalog) Label(l Lang, k Key, v Vars) string {
	s, _ := c.find(l, k)
	return fill(s, v, func(s string) string { return s })
}

// fill puts v into the template in one pass, so a value holding braces stays as it is.
func fill(template string, v Vars, literal func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range placeholder.FindAllStringIndex(template, -1) {
		b.WriteString(literal(template[last:m[0]]))
		token := template[m[0]:m[1]]
		if val, ok := v[token[1:len(token)-1]]; ok {
			b.WriteString(val)
		} else {
			b.WriteString(literal(token))
		}
		last = m[1]
	}
	b.WriteString(literal(template[last:]))
	return b.String()
}

// escapeMarkdown puts a backslash before each ASCII punctuation mark, which CommonMark reads as
// that mark and nothing more.
func escapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
