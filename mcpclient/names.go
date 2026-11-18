package mcpclient

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/text"
)

// Limits on what a server may list (R-MCP-4, R-MCP-7). A tool past one is
// skipped and reported, never taken in part.
const (
	// MaxTools is the most tools taken from one server.
	MaxTools = 128
	// MaxDescription is the longest description, in runes.
	MaxDescription = 8000
	// MaxSchema is the biggest input schema, in bytes of its raw JSON.
	MaxSchema = 32 << 10
	// MaxListBytes is the biggest tool list, all pages together.
	MaxListBytes = 512 << 10
	// MaxName is the longest full tool name, "<server>.<tool>", in runes.
	MaxName = 64
	// MaxResult is the longest tool result taken whole, in bytes.
	MaxResult = 1 << 20
	// MaxCallBytes is the biggest answer to a call that is read.
	MaxCallBytes = 4 << 20
)

// SkipReason is why a listed tool was not taken. Stored nowhere, shown on
// the card of the connect; the values are stable.
type SkipReason string

// Reasons.
const (
	SkipTooMany     SkipReason = "too_many"    // past the 128th
	SkipDescription SkipReason = "description" // over MaxDescription
	SkipSchema      SkipReason = "schema"      // over MaxSchema
	SkipName        SkipReason = "name"        // the full name is over MaxName
	SkipCollision   SkipReason = "collision"   // the name is an earlier tool's
	SkipEmpty       SkipReason = "empty"       // no usable name
	SkipBadSchema   SkipReason = "bad_schema"  // the input schema is not a JSON object
)

// Skip is a listed tool that was not taken.
type Skip struct {
	Name   string // the server's own name, cut to 64 runes
	Reason SkipReason
}

// Tool is a tool a server lists, as the node takes it.
type Tool struct {
	Own         string // the server's own name, the one tools/call needs
	Short       string // Own as ToolName makes it; the key of the policies
	Name        string // the registry's name, "<server>.<Short>"
	Title       string
	Description string
	InputSchema json.RawMessage // always a JSON object that declares "properties"
	ReadOnly    bool            // annotations.readOnlyHint
	Destructive bool            // annotations.destructiveHint
}

// Listed is what Parse takes from a server's tool list.
type Listed struct {
	Tools   []Tool
	Skipped []Skip
}

// ToolName is the node's name for a server's tool, without the server's
// prefix: the tool's own name without a leading "<server>_" (any case),
// lower-cased, every character outside [a-z0-9_] turned into "_" (R-MCP-4).
func ToolName(server, own string) string {
	short := own
	if p := server + "_"; len(own) >= len(p) && strings.EqualFold(own[:len(p)], p) {
		short = own[len(p):]
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(short))
}

// wireTool is a tool as the server's list has it.
type wireTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnly    bool `json:"readOnlyHint"`
		Destructive bool `json:"destructiveHint"`
	} `json:"annotations"`
}

// emptySchema is the schema of a tool that lists none.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// Parse takes the tools of server from the raw entries of its list, in
// order, within the limits above (R-MCP-4). The skipped ones are named; a
// name that collides with an earlier tool's is the later one skipped.
func Parse(server string, raw []json.RawMessage) Listed {
	var out Listed
	seen := map[string]bool{}
	for i, r := range raw {
		var w wireTool
		if json.Unmarshal(r, &w) != nil {
			out.Skipped = append(out.Skipped, Skip{Reason: SkipEmpty})
			continue
		}
		t, why := takeTool(server, w, i, seen)
		if why != "" {
			out.Skipped = append(out.Skipped, Skip{Name: cut64(w.Name), Reason: why})
			continue
		}
		seen[t.Name] = true
		out.Tools = append(out.Tools, t)
	}
	return out
}

// takeTool checks w against the limits; why is "" when it is taken.
func takeTool(server string, w wireTool, index int, seen map[string]bool) (t Tool, why SkipReason) {
	short := ToolName(server, w.Name)
	full := server + "." + short
	switch {
	case index >= MaxTools:
		return t, SkipTooMany
	case short == "":
		return t, SkipEmpty
	case utf8.RuneCountInString(full) > MaxName:
		return t, SkipName
	case utf8.RuneCountInString(w.Description) > MaxDescription:
		return t, SkipDescription
	case len(w.InputSchema) > MaxSchema:
		return t, SkipSchema
	case seen[full]:
		return t, SkipCollision
	}
	schema, ok := normalSchema(w.InputSchema)
	if !ok {
		return t, SkipBadSchema
	}
	return Tool{Own: w.Name, Short: short, Name: full, Title: w.Title, Description: w.Description, InputSchema: schema,
		ReadOnly: w.Annotations.ReadOnly, Destructive: w.Annotations.Destructive}, ""
}

// normalSchema is the schema as a JSON object that declares "properties",
// which tool.CheckArgs needs to accept any call; false when it is not an
// object. No schema is the empty one.
func normalSchema(raw json.RawMessage) (json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == jsonNull {
		return emptySchema, true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	if _, ok := m["properties"]; ok {
		return raw, true
	}
	m["properties"] = json.RawMessage(`{}`)
	out, err := json.Marshal(m)
	return out, err == nil
}

// cut64 is s cut to 64 runes, for a name that is only reported.
func cut64(s string) string {
	if r := []rune(s); len(r) > 64 {
		return string(r[:64])
	}
	return s
}

// firstLine is the first line of s cut to n runes.
func firstLine(s string, n int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return text.Truncate(strings.TrimSpace(line), n)
}
