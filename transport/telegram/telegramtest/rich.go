package telegramtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// The limits of a rich message that the fake enforces, as Telegram does.
const (
	maxRichChars   = 32768
	maxRichBlocks  = 500
	maxRichDepth   = 16
	maxRichColumns = 20
)

// richBlocks are the block types the node may send. Anything else is refused, so
// a test fails when a block outside the allowlist comes out (ADR-0054).
var richBlocks = map[string]bool{
	"paragraph": true, "heading": true, "pre": true, "list": true, "blockquote": true,
	"expandable_blockquote": true, "divider": true, "table": true,
	// footer is the node's signature under a module's message (ADR-0074);
	// nothing in a message's text makes one.
	"footer": true,
}

// sendRichMessage takes a rich message described by blocks. Text in markdown or
// html is refused here, unlike on Telegram, because the node must never send it.
func (s *Server) sendRichMessage(w http.ResponseWriter, params map[string]json.RawMessage) {
	var p struct {
		ChatID              int64 `json:"chat_id"`
		MessageThreadID     int64 `json:"message_thread_id"`
		DisableNotification bool  `json:"disable_notification"`
		RichMessage         struct {
			Markdown *string `json:"markdown"`
			HTML     *string `json:"html"`
			Blocks   []any   `json:"blocks"`
		} `json:"rich_message"`
		ReplyParameters *struct {
			MessageID int64 `json:"message_id"`
		} `json:"reply_parameters"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "Bad Request: "+err.Error(), 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg := s.richRefusal(p.ChatID, p.MessageThreadID); msg != "" {
		writeErr(w, http.StatusBadRequest, "Bad Request: "+msg, 0)
		return
	}
	if p.RichMessage.Markdown != nil || p.RichMessage.HTML != nil {
		writeErr(w, http.StatusBadRequest, "Bad Request: only blocks may be sent", 0)
		return
	}
	r := &richReader{}
	text := r.blocks(p.RichMessage.Blocks, 1)
	switch {
	case r.err != "":
		writeErr(w, http.StatusBadRequest, "Bad Request: "+r.err, 0)
		return
	case text == "":
		writeErr(w, http.StatusBadRequest, "Bad Request: message is empty", 0)
		return
	case r.chars > maxRichChars || r.count > maxRichBlocks:
		writeErr(w, http.StatusBadRequest, "Bad Request: rich message is too long", 0)
		return
	}
	out := Outgoing{ChatID: p.ChatID, ThreadID: p.MessageThreadID, Text: text, Silent: p.DisableNotification, Rich: true, Footer: r.footer}
	if p.ReplyParameters != nil {
		out.ReplyTo = p.ReplyParameters.MessageID
	}
	s.keep(w, out)
}

func (s *Server) richRefusal(chat, thread int64) string {
	switch {
	case chat == 0:
		return "chat_id is empty"
	case thread != 0 && !s.threads[chat][thread]:
		return "message thread not found"
	}
	return ""
}

// richReader flattens blocks to the plain text a person would read, and checks
// them.
type richReader struct {
	chars, count int
	err          string
	footer       string // the text of the last footer block
}

func (r *richReader) blocks(blocks []any, depth int) string {
	if depth > maxRichDepth {
		r.err = "too many nested levels"
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		m, _ := b.(map[string]any)
		parts = append(parts, r.block(m, depth))
	}
	return strings.Join(parts, "\n\n")
}

func (r *richReader) block(m map[string]any, depth int) string {
	kind, _ := m["type"].(string)
	if !richBlocks[kind] {
		r.err = fmt.Sprintf("block type %q is not allowed", kind)
		return ""
	}
	r.count++
	inner, _ := m["blocks"].([]any)
	switch kind {
	case "divider":
		return "———"
	case "blockquote":
		return r.blocks(inner, depth+1)
	case "list":
		return r.list(m, depth)
	case "table":
		return r.table(m)
	case "footer":
		r.footer = r.text(m["text"])
		return r.footer
	default:
		return r.text(m["text"])
	}
}

func (r *richReader) list(m map[string]any, depth int) string {
	items, _ := m["items"].([]any)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		r.count++
		it, _ := item.(map[string]any)
		inner, _ := it["blocks"].([]any)
		lines = append(lines, r.blocks(inner, depth+1))
	}
	return strings.Join(lines, "\n")
}

func (r *richReader) table(m map[string]any) string {
	rows, _ := m["cells"].([]any)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		r.count++
		cells, _ := row.([]any)
		if len(cells) > maxRichColumns {
			r.err = "too many table columns"
		}
		texts := make([]string, 0, len(cells))
		for _, c := range cells {
			cell, _ := c.(map[string]any)
			texts = append(texts, r.text(cell["text"]))
		}
		lines = append(lines, strings.Join(texts, " | "))
	}
	return strings.Join(lines, "\n")
}

// text flattens rich text: a string, a list, or an object with a text.
func (r *richReader) text(t any) string {
	switch t := t.(type) {
	case string:
		r.chars += len([]rune(t))
		return t
	case []any:
		var b strings.Builder
		for _, p := range t {
			b.WriteString(r.text(p))
		}
		return b.String()
	case map[string]any:
		return r.text(t["text"])
	}
	return ""
}
