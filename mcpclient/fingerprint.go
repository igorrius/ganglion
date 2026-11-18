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

// Fingerprint is the pin of a tool list: SHA-256 over every listed tool,
// the skipped ones too, each reduced to the members Parse reads (covered)
// in canonical JSON, sorted by name. The order of the tools and of the
// keys does not change it, and neither does Parse's result; any change of
// a member's text does (R-MCP-7). An entry that is not a JSON object counts
// as its own bytes.
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

// reduce is a tool entry's name and canonical JSON of its covered members,
// the same members, by the same exact names, that Parse reads.
func reduce(r json.RawMessage) (name, canonical string) {
	kept, ok := members(r)
	if !ok {
		return "", string(bytes.TrimSpace(r))
	}
	_ = json.Unmarshal(kept[memberName], &name)
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
