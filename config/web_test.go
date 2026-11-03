package config

import (
	"context"
	"log/slog"
	"testing"

	"github.com/igorrius/ganglion/tool"
	"github.com/samber/do/v2"
)

func TestWebToolsFollowTheSettings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		key       string
		off       bool
		hasSearch bool
	}{
		{"key-free search by default", "", false, true},
		{"Brave with a key", "BSA-key", false, true},
		{"search off", "BSA-key", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := agentConfig(t)
			b.Agent.BraveKey, b.Agent.SearchOff = tc.key, tc.off
			i := New(context.Background(), b, slog.New(slog.DiscardHandler))
			t.Cleanup(func() { _ = i.Shutdown() })
			reg, err := do.Invoke[*tool.Registry](i)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := reg.Spec("web.fetch"); !ok {
				t.Error("web.fetch is not registered")
			}
			if _, ok := reg.Spec("web.search"); ok != tc.hasSearch {
				t.Errorf("web.search registered = %v, want %v", ok, tc.hasSearch)
			}
		})
	}
}

// Every tool of M3b is for admins only (design section 4, rule 8): a
// spec without AdminOnly would be offered to everyone.
func TestEveryToolIsAdminOnly(t *testing.T) {
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	reg, err := do.Invoke[*tool.Registry](i)
	if err != nil {
		t.Fatal(err)
	}
	if specs := reg.Specs(false); len(specs) != 0 {
		t.Fatalf("tools offered to non-admins: %+v", specs)
	}
	if len(reg.Specs(true)) == 0 {
		t.Fatal("no tools at all")
	}
}
