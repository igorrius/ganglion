package hostcmd

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// Show is argv as one line a person reads in a question, a plan or an
// alert: plain arguments as they are, others in single quotes, so a space
// or a quote inside an argument cannot make it look like two. An argument
// with a character that cannot be seen as itself (a newline, a control, a
// bidi or zero-width mark, an odd space) is written as bash's $'...' with
// escapes, so it cannot draw a fake line or hide text.
func Show(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		switch {
		case plainArg.MatchString(a):
			parts[i] = a
		case hidesText(a):
			parts[i] = ansiC(a)
		default:
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// unseen reports whether r cannot be read as itself in a line: not a
// graphic rune, or a space other than the ASCII one.
func unseen(r rune) bool {
	return !unicode.IsGraphic(r) || (r != ' ' && unicode.Is(unicode.Z, r))
}

func hidesText(a string) bool {
	if !utf8.ValidString(a) {
		return true
	}
	return strings.IndexFunc(a, unseen) >= 0
}

// ansiC writes a as a $'...' word.
func ansiC(a string) string {
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(a); {
		r, size := utf8.DecodeRuneInString(a[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, a[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\'':
			b.WriteString(`\'`)
		case r < 0x80 && unseen(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xFFFF && unseen(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		case unseen(r):
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	b.WriteString("'")
	return b.String()
}
