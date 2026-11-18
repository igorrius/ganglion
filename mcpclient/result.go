package mcpclient

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/text"
)

// CallResult is a tool's answer as the node reads it: one text, whatever
// the content blocks were. It is outside content, whatever IsError says
// (R-MCP-4).
type CallResult struct {
	Text    string
	IsError bool // the tool reported an error, in its own words
}

// wireResult is the result of tools/call.
type wireResult struct {
	Content           []block         `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// block is one content block.
type block struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	MIMEType string `json:"mimeType"`
	URI      string `json:"uri"`
	Resource *struct {
		URI  string  `json:"uri"`
		Text *string `json:"text"`
	} `json:"resource"`
}

// maxLabel is the longest address or type a placeholder line names.
const maxLabel = 200

// parseResult reads the result of tools/call into a CallResult, cut to
// MaxResult bytes with a note (R-MCP-8).
func parseResult(raw json.RawMessage) (CallResult, error) {
	var w wireResult
	if err := json.Unmarshal(raw, &w); err != nil {
		return CallResult{}, fmt.Errorf("%w: the result is not an object", ErrProtocol)
	}
	var lines []string
	for _, b := range w.Content {
		lines = append(lines, blockText(b))
	}
	body := strings.Join(lines, "\n")
	if len(w.Content) == 0 && len(w.StructuredContent) > 0 && string(w.StructuredContent) != jsonNull {
		body = string(w.StructuredContent)
	}
	return CallResult{Text: cutResult(body), IsError: w.IsError}, nil
}

// blockText is a content block as text. What the model cannot read is
// named, not carried: an image or a blob would only fill the context.
func blockText(b block) string {
	switch b.Type {
	case "text":
		return b.Text
	case "image", "audio":
		return "[" + b.Type + " " + text.Truncate(b.MIMEType, maxLabel) + ", not shown]"
	case "resource_link":
		return "[resource link: " + text.Truncate(b.URI, maxLabel) + "]"
	case "resource":
		if b.Resource != nil && b.Resource.Text != nil {
			return *b.Resource.Text
		}
		uri := ""
		if b.Resource != nil {
			uri = b.Resource.URI
		}
		return "[resource " + text.Truncate(uri, maxLabel) + ", binary, not shown]"
	}
	return "[" + text.Truncate(b.Type, maxLabel) + " content, not shown]"
}

// cutResult is s cut at a rune boundary to MaxResult bytes, with a note of
// its real size.
func cutResult(s string) string {
	if len(s) <= MaxResult {
		return s
	}
	end := MaxResult
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + fmt.Sprintf("\n[cut: the result was %d bytes; only the first 1 MB is shown]", len(s))
}
