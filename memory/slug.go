package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxSlugLen caps a slug, its "-2" suffix included.
const MaxSlugLen = 60

// slugPattern is a slug: lower-case ASCII words joined by single dashes.
// It cannot hold "/", "." or "..", so a slug never leaves memory/.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// checkSlug refuses anything that is not a slug.
func checkSlug(slug string) error {
	if len(slug) > MaxSlugLen || !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w: %q is not a fact's slug (lower-case letters, digits and dashes)", ErrInvalid, slug)
	}
	return nil
}

// translit spells Ukrainian and Russian letters in ASCII (after the
// Ukrainian national system, simplified: one spelling per letter).
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "h", 'ґ': "g", 'д': "d", 'е': "e", 'є': "ye", 'ж': "zh", 'з': "z",
	'и': "y", 'і': "i", 'ї': "yi", 'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p",
	'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ь': "", 'ю': "yu", 'я': "ya", 'ё': "yo", 'ы': "y", 'э': "e", 'ъ': "",
}

// Slug derives a fact's slug from its title: lower case, Cyrillic
// transliterated, every other run of characters a dash, at most
// MaxSlugLen bytes. A title with nothing left (another script) gets
// "fact-" and a short hash of the title.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		s, ok := translit[r]
		switch {
		case ok && s == "":
			continue
		case ok:
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9'):
			s = string(r)
		default:
			dash = b.Len() > 0
			continue
		}
		if dash {
			b.WriteByte('-')
			dash = false
		}
		b.WriteString(s)
	}
	if b.Len() == 0 {
		sum := sha256.Sum256([]byte(title))
		return "fact-" + hex.EncodeToString(sum[:4])
	}
	return cut(b.String(), MaxSlugLen)
}

// cut shortens slug to at most n bytes without a dash at the end.
func cut(slug string, n int) string {
	if len(slug) > n {
		slug = slug[:n]
	}
	return strings.TrimRight(slug, "-")
}

// allocate returns the slug for title among the entries: the title's
// own slug, or with "-2", "-3"… when that one holds a fact with another
// title. same are the entries (stored or quarantined) that already hold
// this title.
func allocate(title string, all []Entry) (slug string, same []Entry) {
	base := Slug(title)
	for n := 1; ; n++ {
		slug = base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			slug = cut(base, MaxSlugLen-len(suffix)) + suffix
		}
		taken := false
		same = same[:0]
		for _, e := range all {
			if e.Slug != slug {
				continue
			}
			if strings.EqualFold(e.Title, title) {
				same = append(same, e)
			} else {
				taken = true
			}
		}
		if !taken || len(same) > 0 {
			return slug, same
		}
	}
}
