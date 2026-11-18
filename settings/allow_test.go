package settings_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

func TestAllowedIsTheSpecList(t *testing.T) {
	want := []string{"agent.call_timeout", "agent.languages", "agent.limits.daily_budget_usd",
		"agent.limits.daily_tokens", "agent.limits.daily_turns", "agent.search.mode",
		"agent.session_idle", "agent.turn_timeout", "log.level", "memory.prompt_budget",
		"memory.review.enabled", "memory.review.quiet", "models.purposes", "node.memory_limit",
		"node.tz", "telegram.reactions.after", "telegram.reactions.dwell", "telegram.reply",
		"telegram.rich", "update.check"}
	if !slices.Equal(settings.AllowedPaths, want) {
		t.Fatalf("AllowedPaths = %v", settings.AllowedPaths)
	}
	for _, p := range want {
		if !settings.Allowed(p) {
			t.Errorf("Allowed(%q) = false", p)
		}
	}
	if settings.Allowed("agent.limits") || settings.Allowed("sandbox.deny") {
		t.Error("a section or a host field is allowed")
	}
}

// TestEveryAllowedPathIsAField walks the loader's types by their yaml
// tags; TestSchemaMatchesTheLoader keeps those in step with the schema.
func TestEveryAllowedPathIsAField(t *testing.T) {
	for _, p := range settings.AllowedPaths {
		typ := reflect.TypeFor[settings.File]()
		for seg := range strings.SplitSeq(p, ".") {
			if typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			f, ok := fieldByTag(typ, seg)
			if !ok {
				t.Errorf("%s: no field %q in %s", p, seg, typ)
				break
			}
			typ = f.Type
		}
	}
}

func fieldByTag(typ reflect.Type, tag string) (reflect.StructField, bool) {
	if typ.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for f := range typ.Fields() {
		if strings.Split(f.Tag.Get("yaml"), ",")[0] == tag {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func TestWhere(t *testing.T) {
	for path, want := range map[string]settings.Place{
		"agent.limits.daily_turns": settings.PlaceChat,
		"telegram.token":           settings.PlaceHost,
		"telegram.admin":           settings.PlaceHost,
		"telegram.api_url":         settings.PlaceHost,
		"models.providers":         settings.PlaceHost,
		"sandbox.deny":             settings.PlaceHost,
		"http.public_url":          settings.PlaceOwnTool,
		"telegram.updates":         settings.PlaceOwnTool,
		"telegram.groups":          settings.PlaceOwnTool,
		"telegram.groups[0].role":  settings.PlaceOwnTool,
		"mcp.servers":              settings.PlaceOwnTool,
		"mcp.page":                 settings.PlaceOwnTool,
		"destinations":             settings.PlaceOwnTool,
		"modules":                  settings.PlaceOwnTool,
		"agent.limits":             settings.PlaceHost,
		"httpx":                    settings.PlaceHost,
	} {
		if got := settings.Where(path); got != want {
			t.Errorf("Where(%q) = %q, want %q", path, got, want)
		}
	}
}
