package telegram

import (
	"strconv"
	"unicode/utf16"

	"github.com/igorrius/ganglion/event"
)

// maxTextUnits is the longest message Telegram accepts, in UTF-16 code
// units.
const maxTextUnits = 4096

// Parts implements transport.Parter. A text within the limit comes back as
// the only part, unchanged. A longer one is cut into chunks of at most
// maxTextUnits: after the last newline in the final quarter of a chunk,
// else after the last space there, else at the limit. A character is never
// cut in half. Part i gets IdemKey + ":p<i>"; the result depends only on
// msg, so a resumed sender gets the same parts.
func (t *Transport) Parts(msg event.Outgoing) []event.Outgoing {
	runes := []rune(msg.Text)
	if textUnits(runes) <= maxTextUnits {
		return []event.Outgoing{msg}
	}
	var parts []event.Outgoing
	for start := 0; start < len(runes); {
		cut := nextCut(runes, start)
		part := msg
		part.IdemKey = msg.IdemKey + ":p" + strconv.Itoa(len(parts))
		part.Text = string(runes[start:cut])
		parts = append(parts, part)
		start = cut
	}
	return parts
}

// nextCut returns the end of the chunk that starts at start.
func nextCut(runes []rune, start int) int {
	// prefix[k] is the size in units of runes[start:start+k].
	prefix := []int{0}
	end := start
	for end < len(runes) {
		u := prefix[len(prefix)-1] + runeUnits(runes[end])
		if u > maxTextUnits {
			break
		}
		prefix = append(prefix, u)
		end++
	}
	if end == len(runes) {
		return end
	}
	const quarter = maxTextUnits * 3 / 4
	for _, sep := range []rune{'\n', ' '} {
		for k := end; k > start && prefix[k-start] >= quarter; k-- {
			if runes[k-1] == sep {
				return k
			}
		}
	}
	return end
}

func runeUnits(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1 // an invalid rune is sent as U+FFFD
}

func textUnits(runes []rune) int {
	n := 0
	for _, r := range runes {
		n += runeUnits(r)
	}
	return n
}
