package agent

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/tool"
)

// filesRule is the core rule for files sent to the agent (R-DLV-10).
const filesRule = `A "[file came: ...]" line means the sender sent a file that you cannot open yet: say so, and answer what you can from their words.`

// sourceFileName is the outside source of a file's name (R-DLV-10).
const sourceFileName = "file name"

// maxFileName caps a file's name in the note, in runes.
const maxFileName = 80

// mimeType is a MIME type as the note shows it; anything else is
// "unknown type".
var mimeType = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,30}/[a-z0-9][a-z0-9.+-]{0,40}$`)

// fileNotes are the lines that tell the model which files came with a
// message. A name is the sender's text, maybe someone else's: it is
// cleaned, capped and wrapped as data, and named reports that one was
// shown, which taints the session.
func fileNotes(fs []event.File) (notes string, named bool) {
	var b strings.Builder
	for _, f := range fs {
		typ := strings.ToLower(f.Type)
		if !mimeType.MatchString(typ) {
			typ = "unknown type"
		}
		fmt.Fprintf(&b, "\n[file came: %s, %s, %s; you cannot open files yet", fileKind(f.Kind), typ, fileSize(f.Size))
		if name := cleanName(f.Name); name != "" {
			b.WriteString("; its name: " + tool.WrapExternal(sourceFileName, name))
			named = true
		}
		b.WriteString("]")
	}
	return b.String(), named
}

// fileKind is the kind as the note shows it; a kind the agent does not
// know is "file".
func fileKind(k event.FileKind) string {
	switch k {
	case event.FilePhoto, event.FileDocument, event.FileAudio, event.FileVoice, event.FileVideo,
		event.FileVideoNote, event.FileAnimation, event.FileSticker:
		return string(k)
	}
	return "file"
}

// cleanName keeps a name on one line: control and other invisible
// characters become spaces, runs of space one, and it is capped.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	return text.Truncate(strings.Join(strings.Fields(s), " "), maxFileName)
}

// fileSize is a size for people: bytes, KB, MB or GB with one decimal.
func fileSize(n int64) string {
	switch {
	case n <= 0:
		return "size unknown"
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
