package mcpclient

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestToolName(t *testing.T) {
	tests := []struct{ server, own, want string }{
		{"shop", "search", "search"},
		{"shop", "shop_search", "search"},
		{"shop", "SHOP_Search", "search"},
		{"shop", "shopping", "shopping"},
		{"shop", "Add-To.Cart", "add_to_cart"},
		{"shop", "shop_", ""},
		{"shop", "список", "______"},
		{"shop", "a b/c", "a_b_c"},
	}
	for _, tt := range tests {
		if got := ToolName(tt.server, tt.own); got != tt.want {
			t.Errorf("ToolName(%q, %q) = %q, want %q", tt.server, tt.own, got, tt.want)
		}
	}
}

func raw(name, desc, schema string) json.RawMessage {
	if schema == "" {
		schema = `{"type":"object","properties":{}}`
	}
	b, _ := json.Marshal(map[string]any{"name": name, "description": desc, "inputSchema": json.RawMessage(schema)})
	return b
}

func TestParseSkipsWhatIsOverALimit(t *testing.T) {
	long := strings.Repeat("x", MaxDescription+1)
	bigSchema := `{"type":"object","properties":{"a":{"description":"` + strings.Repeat("y", MaxSchema) + `"}}}`
	tests := []struct {
		name string
		in   json.RawMessage
		why  SkipReason
	}{
		{"description", raw("a", long, ""), SkipDescription},
		{"schema", raw("a", "", bigSchema), SkipSchema},
		{"bad schema", raw("a", "", `[1]`), SkipBadSchema},
		{"name", raw(strings.Repeat("n", MaxName), "", ""), SkipName},
		{"empty", raw("shop_", "", ""), SkipEmpty},
		{"not an object", json.RawMessage(`"x"`), SkipEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Parse("shop", []json.RawMessage{tt.in})
			if len(l.Tools) != 0 || len(l.Skipped) != 1 || l.Skipped[0].Reason != tt.why {
				t.Fatalf("Parse = %+v, want one skip %q", l, tt.why)
			}
		})
	}
}

func TestParseCollisionsAndTheCap(t *testing.T) {
	l := Parse("shop", []json.RawMessage{raw("search", "", ""), raw("shop_search", "", ""), raw("Search", "", "")})
	if len(l.Tools) != 1 || l.Tools[0].Name != "shop.search" || l.Tools[0].Own != "search" || len(l.Skipped) != 2 || l.Skipped[0].Reason != SkipCollision {
		t.Fatalf("collisions: %+v", l)
	}
	var many []json.RawMessage
	for i := range MaxTools + 2 {
		many = append(many, raw(fmt.Sprintf("t%d", i), "", ""))
	}
	l = Parse("shop", many)
	if len(l.Tools) != MaxTools || len(l.Skipped) != 2 || l.Skipped[0].Reason != SkipTooMany {
		t.Fatalf("cap: %d tools, skipped %+v", len(l.Tools), l.Skipped)
	}
}

func TestParseTakesAnnotationsAndNormalisesTheSchema(t *testing.T) {
	in := json.RawMessage(`{"name":"buy","title":"Buy","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true,"destructiveHint":true}}`)
	l := Parse("shop", []json.RawMessage{in, json.RawMessage(`{"name":"bare"}`)})
	if len(l.Tools) != 2 {
		t.Fatalf("%+v", l)
	}
	buy := l.Tools[0]
	if !buy.ReadOnly || !buy.Destructive || buy.Title != "Buy" || !strings.Contains(string(buy.InputSchema), `"properties"`) {
		t.Fatalf("buy = %+v (schema %s)", buy, buy.InputSchema)
	}
	if string(l.Tools[1].InputSchema) != string(emptySchema) {
		t.Fatalf("bare schema = %s", l.Tools[1].InputSchema)
	}
}

func TestFingerprint(t *testing.T) {
	a := json.RawMessage(`{"name":"a","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}},"annotations":{"readOnlyHint":true}}`)
	b := json.RawMessage(`{"name":"b","inputSchema":{"type":"object"}}`)
	aReordered := json.RawMessage(`{ "annotations": {"readOnlyHint": true}, "inputSchema": {"properties": {"y": {"type": "integer"}, "x": {"type": "string"}}, "type": "object"}, "description": "d", "name": "a", "extra": 1 }`)
	base := Fingerprint([]json.RawMessage{a, b})
	if !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Fatalf("fingerprint = %q", base)
	}
	if got := Fingerprint([]json.RawMessage{b, aReordered}); got != base {
		t.Errorf("order of tools and keys, and an uncovered field, changed it: %s", got)
	}
	changes := map[string]json.RawMessage{
		"description": json.RawMessage(`{"name":"a","description":"d!","inputSchema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}},"annotations":{"readOnlyHint":true}}`),
		"schema":      json.RawMessage(`{"name":"a","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"integer"}}},"annotations":{"readOnlyHint":true}}`),
		"annotation":  json.RawMessage(`{"name":"a","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}},"annotations":{"readOnlyHint":false}}`),
		"title":       json.RawMessage(`{"name":"a","title":"A","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}},"annotations":{"readOnlyHint":true}}`),
		"name":        json.RawMessage(`{"name":"a2","description":"d","inputSchema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}},"annotations":{"readOnlyHint":true}}`),
	}
	for what, c := range changes {
		if got := Fingerprint([]json.RawMessage{c, b}); got == base {
			t.Errorf("a changed %s left the fingerprint as it was", what)
		}
	}
	if Fingerprint([]json.RawMessage{a}) == base || Fingerprint([]json.RawMessage{a, b, b}) == base {
		t.Error("a tool more or less left the fingerprint as it was")
	}
}

func TestParseResult(t *testing.T) {
	tests := []struct {
		name, in, want string
		isErr          bool
	}{
		{"text", `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`, "a\nb", false},
		{"image", `{"content":[{"type":"image","mimeType":"image/png","data":"AAAA"}]}`, "[image image/png, not shown]", false},
		{"audio", `{"content":[{"type":"audio","mimeType":"audio/wav","data":"AAAA"}]}`, "[audio audio/wav, not shown]", false},
		{"link", `{"content":[{"type":"resource_link","uri":"file:///a"}]}`, "[resource link: file:///a]", false},
		{"resource text", `{"content":[{"type":"resource","resource":{"uri":"u","text":"body"}}]}`, "body", false},
		{"resource blob", `{"content":[{"type":"resource","resource":{"uri":"u","blob":"AAAA"}}]}`, "[resource u, binary, not shown]", false},
		{"unknown", `{"content":[{"type":"hologram"}]}`, "[hologram content, not shown]", false},
		{"structured", `{"content":[],"structuredContent":{"b":1,"a":2}}`, `{"b":1,"a":2}`, false},
		{"error", `{"content":[{"type":"text","text":"nope"}],"isError":true}`, "nope", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseResult(json.RawMessage(tt.in))
			if err != nil || got.Text != tt.want || got.IsError != tt.isErr {
				t.Fatalf("parseResult = %+v, %v; want %q (error %v)", got, err, tt.want, tt.isErr)
			}
		})
	}
	if _, err := parseResult(json.RawMessage(`[1]`)); err == nil {
		t.Fatal("a result that is not an object was taken")
	}
}

func TestCutResultAtARuneBoundary(t *testing.T) {
	s := strings.Repeat("я", MaxResult) // two bytes each
	got := cutResult(s)
	body, note, ok := strings.Cut(got, "\n[cut: ")
	if !ok || len(body) > MaxResult || strings.ContainsRune(body, '�') || !strings.Contains(note, fmt.Sprint(len(s))) {
		t.Fatalf("cut = %d bytes, note %q", len(body), note)
	}
	if cutResult("short") != "short" {
		t.Fatal("a short result was changed")
	}
}
