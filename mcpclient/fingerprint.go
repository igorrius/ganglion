package mcpclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/tool"
)

// fingerprinted are the fields of a listed tool the fingerprint covers: all
// the text the model reads and the schema it calls by (R-MCP-7).
var fingerprinted = []string{"name", "title", "description", "inputSchema", "annotations"}

// Fingerprint is the pin of a tool list: SHA-256 over every listed tool,
// the skipped ones too, each reduced to its name, title, description,
// input schema and annotations, in canonical JSON and sorted by name. The
// order of the tools and of the keys does not change it; any change of a
// field's text does (R-MCP-7). An entry that is not a JSON object counts as
// its own bytes.
func Fingerprint(raw []json.RawMessage) string {
	type entry struct{ name, canonical string }
	entries := make([]entry, len(raw))
	for i, r := range raw {
		name, canonical := reduce(r)
		entries[i] = entry{name, canonical}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.canonical, b.canonical)
	})
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.canonical
	}
	sum := sha256.Sum256([]byte("[" + strings.Join(parts, ",") + "]"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// reduce is a tool entry's name and canonical JSON of the covered fields.
func reduce(r json.RawMessage) (name, canonical string) {
	var m map[string]json.RawMessage
	if json.Unmarshal(r, &m) != nil || m == nil {
		return "", string(bytes.TrimSpace(r))
	}
	kept := make(map[string]json.RawMessage, len(fingerprinted))
	for _, k := range fingerprinted {
		if v, ok := m[k]; ok {
			kept[k] = v
		}
	}
	_ = json.Unmarshal(kept["name"], &name)
	b, err := json.Marshal(kept)
	if err != nil {
		return name, string(bytes.TrimSpace(r))
	}
	canonical, err = tool.CanonicalArgs(b)
	if err != nil {
		return name, string(b)
	}
	return name, canonical
}
