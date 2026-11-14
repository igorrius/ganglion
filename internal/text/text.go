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

// CodeSlashWords puts each word of s that Telegram would read as a bot
// command, "/tmp/x" or "~/src", in a code span, so a path in the agent's
// words shows as a path and not as a command link: Telegram finds commands
// in a bot's message itself and leaves only code alone. Code spans already
// in s, and words where the slash follows a letter or a digit ("a/b", "1/2"),
// are kept as they are, and so are the marks around a word.
func CodeSlashWords(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(codeWords(s))
			break
		}
		b.WriteString(codeWords(s[:i]))
		s = s[i:]
		n := len(s) - len(strings.TrimLeft(s, "`"))
		end := closingRun(s[n:], n)
		if end < 0 {
			b.WriteString(s[:n])
			s = s[n:]
			continue
		}
		b.WriteString(s[:n+end+n])
		s = s[n+end+n:]
	}
	return b.String()
}

// closingRun is where in s a run of exactly n backticks starts, or -1.
func closingRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return i
		}
		i = j
	}
	return -1
}

// codeWords is CodeSlashWords for text with no code span in it.
func codeWords(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		end := strings.IndexAny(s, " \t\n")
		if end < 0 {
			end = len(s)
		}
		b.WriteString(codeWord(s[:end]))
		gap := len(s[end:]) - len(strings.TrimLeft(s[end:], " \t\n"))
		b.WriteString(s[end : end+gap])
		s = s[end+gap:]
	}
	return b.String()
}

// codeWord is one word, with the marks around it kept outside its span.
func codeWord(w string) string {
	core := strings.TrimLeft(w, `("'«*_[`)
	lead := w[:len(w)-len(core)]
	core = strings.TrimRight(core, `.,;:!?)"'»*_]`)
	if core == "" || !commandLike(core) {
		return w
	}
	return lead + CodeSpan(core) + w[len(lead)+len(core):]
}

// commandLike reports whether Telegram would find a command in w: a slash
// followed by a Latin letter, a digit or "_", and not preceded by a letter
// of any script, a digit, "_", a slash or an angle bracket.
func commandLike(w string) bool {
	for i := 0; i+1 < len(w); i++ {
		if w[i] != '/' || !wordByte(w[i+1]) {
			continue
		}
		if i == 0 {
			return true
		}
		if p := w[i-1]; !wordByte(p) && p < 0x80 && p != '/' && p != '<' && p != '>' {
			return true
		}
	}
	return false
}

func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
