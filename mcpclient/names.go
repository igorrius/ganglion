package mcpclient

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
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
	SkipTooMany     SkipReason = "too_many"    // past the 128th by name
	SkipDescription SkipReason = "description" // over MaxDescription
	SkipSchema      SkipReason = "schema"      // over MaxSchema
	SkipName        SkipReason = "name"        // the full name is over MaxName
	SkipCollision   SkipReason = "collision"   // another listed tool has the same name
	SkipEmpty       SkipReason = "empty"       // no usable name
	SkipBadSchema   SkipReason = "bad_schema"  // the input schema is not a JSON object
	SkipMalformed   SkipReason = "malformed"   // not an object, or a member of the wrong type
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

// covered are the members of a listed tool the node reads and the
// fingerprint covers, by their exact names. A tool is decoded only from
// these members, so a member that differs in case ("DESCRIPTION") is read
// by neither: encoding/json would match it case-insensitively, and the
// model would read text the fingerprint never saw (R-MCP-7).
var covered = []string{memberName, memberTitle, memberDescription, memberSchema, memberAnnotations}

// Members of a listed tool, by their exact names.
const (
	memberName        = "name"
	memberTitle       = "title"
	memberDescription = "description"
	memberSchema      = "inputSchema"
	memberAnnotations = "annotations"
)

// members is a listed tool's covered members, by their exact names; false
// when the entry is not a JSON object. A member written twice is its last
// value, here and in the fingerprint alike.
func members(r json.RawMessage) (map[string]json.RawMessage, bool) {
	var all map[string]json.RawMessage
	if json.Unmarshal(r, &all) != nil || all == nil {
		return nil, false
	}
	out := make(map[string]json.RawMessage, len(covered))
	for _, k := range covered {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out, true
}

// wireTool is a tool as the server's list has it, read from its covered
// members only.
type wireTool struct {
	Name, Title, Description string
	InputSchema              json.RawMessage
	ReadOnly, Destructive    bool
}

// decodeTool reads a tool from its covered members; false when a member
// has the wrong type. Annotations are read by their exact names too.
func decodeTool(m map[string]json.RawMessage) (wireTool, bool) {
	w := wireTool{InputSchema: m[memberSchema]}
	for k, dst := range map[string]*string{memberName: &w.Name, memberTitle: &w.Title, memberDescription: &w.Description} {
		if !decodeOptional(m[k], dst) {
			return w, false
		}
	}
	if raw, ok := m[memberAnnotations]; ok && string(bytes.TrimSpace(raw)) != jsonNull {
		var a map[string]json.RawMessage
		if json.Unmarshal(raw, &a) != nil {
			return w, false
		}
		if !decodeOptional(a["readOnlyHint"], &w.ReadOnly) || !decodeOptional(a["destructiveHint"], &w.Destructive) {
			return w, false
		}
	}
	return w, true
}

// decodeOptional decodes raw into dst when it is present and not null.
func decodeOptional[T any](raw json.RawMessage, dst *T) bool {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == jsonNull {
		return true
	}
	return json.Unmarshal(raw, dst) == nil
}

// emptySchema is the schema of a tool that lists none.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// Parse takes the tools of server from the raw entries of its list within
// the limits above (R-MCP-4). The result does not depend on the order of
// the entries, as the fingerprint does not (R-MCP-7): every tool of a group
// whose names collide is skipped, not the later ones, and the cap of
// MaxTools keeps the first by name. Tools and skips come sorted by name.
func Parse(server string, raw []json.RawMessage) Listed {
	var out Listed
	byName := map[string][]Tool{}
	for _, r := range raw {
		t, own, why := takeTool(server, r)
		if why != "" {
			out.Skipped = append(out.Skipped, Skip{Name: cut64(own), Reason: why})
			continue
		}
		byName[t.Name] = append(byName[t.Name], t)
	}
	var taken []Tool
	for _, group := range byName {
		if len(group) > 1 {
			for _, t := range group {
				out.Skipped = append(out.Skipped, Skip{Name: cut64(t.Own), Reason: SkipCollision})
			}
			continue
		}
		taken = append(taken, group[0])
	}
	slices.SortFunc(taken, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
	for i, t := range taken {
		if i >= MaxTools {
			out.Skipped = append(out.Skipped, Skip{Name: cut64(t.Own), Reason: SkipTooMany})
			continue
		}
		out.Tools = append(out.Tools, t)
	}
	slices.SortFunc(out.Skipped, func(a, b Skip) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(string(a.Reason), string(b.Reason)))
	})
	return out
}

// takeTool checks one entry against the limits; why is "" when it is
// taken, and own is the server's name for it either way.
func takeTool(server string, r json.RawMessage) (t Tool, own string, why SkipReason) {
	m, ok := members(r)
	if !ok {
		return t, "", SkipMalformed
	}
	w, ok := decodeTool(m)
	if !ok {
		return t, w.Name, SkipMalformed
	}
	short := ToolName(server, w.Name)
	full := server + "." + short
	switch {
	case short == "":
		return t, w.Name, SkipEmpty
	case utf8.RuneCountInString(full) > MaxName:
		return t, w.Name, SkipName
	case utf8.RuneCountInString(w.Description) > MaxDescription:
		return t, w.Name, SkipDescription
	case len(w.InputSchema) > MaxSchema:
		return t, w.Name, SkipSchema
	}
	schema, ok := normalSchema(w.InputSchema)
	if !ok {
		return t, w.Name, SkipBadSchema
	}
	return Tool{Own: w.Name, Short: short, Name: full, Title: w.Title, Description: w.Description, InputSchema: schema,
		ReadOnly: w.ReadOnly, Destructive: w.Destructive}, w.Name, ""
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
