package telegram

import (
	"encoding/json"
	"strings"

	textutil "github.com/igorrius/ganglion/internal/text"
)

// richNothingReadable stands for a rich message with no text in it, so that it is
// noted rather than dropped (R-DLV-12).
const richNothingReadable = "[a rich message with nothing readable]"

// richFiles are the blocks that hold a file, and what the agent is told of it. A
// caption is read too; the file and its id never are.
var richFiles = map[string]string{
	"photo":      "[a photo was here]",
	"video":      "[a video was here]",
	"audio":      "[an audio file was here]",
	"voice_note": "[a voice note was here]",
	"animation":  "[an animation was here]",
	"document":   "[a document was here]",
}

// readRich flattens a rich message that came to the bot into plain text, walking
// only the block types it knows and only their text fields: buttons, maps, a
// draft's thinking and any type unknown here add nothing. It returns "" when there
// is nothing to read or the JSON is not a rich message. Everything it returns is
// outside content, hidden blocks included (R-DLV-12).
func readRich(raw json.RawMessage) string {
	var m struct {
		Blocks []any `json:"blocks"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return textutil.Truncate(readBlocks(m.Blocks, 1), maxRichChars-1) // it adds a mark when it cuts
}

func readBlocks(blocks []any, depth int) string {
	if depth > maxRichDepth {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok {
			parts = append(parts, readBlock(m, depth))
		}
	}
	return joinRead("\n\n", parts...)
}

func readBlock(m map[string]any, depth int) string {
	kind, _ := m["type"].(string)
	inner := func() string {
		blocks, _ := m["blocks"].([]any)
		return readBlocks(blocks, depth+1)
	}
	switch kind {
	case "paragraph", "heading", "pre", "footer", "pullquote", "expandable_blockquote":
		return joinRead("\n", readText(m["text"], depth), readText(m["credit"], depth))
	case "blockquote":
		return joinRead("\n", inner(), readText(m["credit"], depth))
	case "collage", "slideshow":
		return joinRead("\n", inner(), readCaption(m["caption"], depth))
	case "details":
		return joinRead("\n", readText(m["summary"], depth), inner())
	case "list":
		return readList(m, depth)
	case "table":
		return readTable(m, depth)
	case "mathematical_expression":
		s, _ := m["expression"].(string)
		return s
	}
	if word, ok := richFiles[kind]; ok {
		return joinRead("\n", word, readCaption(m["caption"], depth))
	}
	return ""
}

func readList(m map[string]any, depth int) string {
	items, _ := m["items"].([]any)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		it, _ := item.(map[string]any)
		label, _ := it["label"].(string)
		blocks, _ := it["blocks"].([]any)
		lines = append(lines, joinRead(" ", label, readBlocks(blocks, depth+1)))
	}
	return joinRead("\n", lines...)
}

func readTable(m map[string]any, depth int) string {
	rows, _ := m["cells"].([]any)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		cells, _ := row.([]any)
		texts := make([]string, 0, len(cells))
		for _, c := range cells {
			cell, _ := c.(map[string]any)
			texts = append(texts, readText(cell["text"], depth))
		}
		lines = append(lines, strings.Join(texts, " | "))
	}
	return joinRead("\n", joinRead("\n", lines...), readText(m["caption"], depth))
}

// readCaption reads a RichBlockCaption, which has a credit beside its text.
func readCaption(v any, depth int) string {
	m, _ := v.(map[string]any)
	return joinRead("\n", readText(m["text"], depth), readText(m["credit"], depth))
}

// readText flattens rich text: a string, a list, or an object with a text.
func readText(v any, depth int) string {
	if depth > maxRichDepth {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			b.WriteString(readText(p, depth+1))
		}
		return b.String()
	case map[string]any:
		return readText(v["text"], depth+1)
	}
	return ""
}

// joinRead joins the parts that have something in them.
func joinRead(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
