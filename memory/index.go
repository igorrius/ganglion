package memory

import (
	"fmt"
	"strings"
)

// MaxIndexEntries caps the index: it goes into every admin's system
// prompt. The rest is found with memory.recall.
const MaxIndexEntries = 200

// Index is the known-facts block of the system prompt: the title and
// slug of every stored fact, in slug order, marked with TaintedMark when
// it was written in a session with outside content, quarantined ones
// left out;
// "" when there is none. The same memory gives the same text, so the
// prompt's cache keeps hitting. An unreadable directory is logged and
// gives "".
func (m *Memory) Index() string {
	es, err := m.list(CheckOK)
	if err != nil {
		m.log.Warn("memory: index unreadable", "err", err)
		return ""
	}
	return index(es)
}

// index renders the entries, already in slug order.
func index(es []Entry) string {
	if len(es) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(IndexHeader)
	for i, e := range es {
		if i == MaxIndexEntries {
			fmt.Fprintf(&b, "- … and %d more; memory.recall finds them\n", len(es)-i)
			break
		}
		fmt.Fprintf(&b, "- %s (%s)", e.Title, e.Slug)
		if e.Tainted {
			b.WriteString(" " + TaintedMark)
		}
		b.WriteString("\n")
	}
	return b.String()
}
