package settings_test

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

const pathSrc = `# the owner's comment
agent:
  limits:
    daily_turns: 200 # keep low
telegram:
  rich: true
`

func TestApplyReturnsTheEditedFile(t *testing.T) {
	out, err := settings.Apply([]byte(pathSrc), []settings.Change{{Path: "telegram.rich", Value: false}})
	if err != nil || !strings.Contains(string(out), "rich: false") || !strings.Contains(string(out), "# keep low") {
		t.Fatalf("Apply = %q, %v", out, err)
	}
}

func TestSetPathKeepsCommentsAndSetsNested(t *testing.T) {
	path := writeFile(t, pathSrc)
	err := settings.SetPath(path, []settings.Change{
		{Path: "agent.limits.daily_turns", Value: float64(300)},
		{Path: "memory.review.quiet", Value: "2h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	for _, want := range []string{"# the owner's comment", "daily_turns: 300 # keep low", "quiet: 2h", "rich: true"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestSetPathRefusesABadValueAndKeepsTheFile(t *testing.T) {
	path := writeFile(t, pathSrc)
	err := settings.SetPath(path, []settings.Change{{Path: "agent.limits.daily_turns", Value: "many"}})
	if err == nil || !strings.Contains(err.Error(), "agent.limits.daily_turns") {
		t.Fatalf("err = %v, want one naming the path", err)
	}
	if read(t, path) != pathSrc {
		t.Fatal("the file changed")
	}
}

func TestSetPathNilRemovesTheKeyAndEmptyParents(t *testing.T) {
	path := writeFile(t, pathSrc)
	if err := settings.SetPath(path, []settings.Change{{Path: "telegram.rich", Value: nil}}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "rich:") || strings.Contains(got, "telegram:") {
		t.Fatalf("rich or an empty telegram is still there:\n%s", got)
	}
}

func TestSetPathSetsAMapping(t *testing.T) {
	src := `models:
  providers:
    - {name: p, type: openai, base_url: "https://example.com/v1", key: env:K}
  models:
    - {id: a, provider: p, name: a, tools: true}
    - {id: b, provider: p, name: b, tools: true}
  purposes:
    chat: [{model: a}]
`
	out, err := settings.Apply([]byte(src), []settings.Change{{Path: "models.purposes",
		Value: map[string]any{"chat": []any{map[string]any{"model": "b"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "model: b") {
		t.Fatalf("purposes not set:\n%s", out)
	}
}

func TestSetPathRefusesAScalarInTheWay(t *testing.T) {
	_, err := settings.Apply([]byte("agent: off\n"), []settings.Change{{Path: "agent.limits.daily_turns", Value: float64(3)}})
	if err == nil {
		t.Fatal("a scalar was walked through")
	}
}

func TestValues(t *testing.T) {
	got, err := settings.Values([]byte(pathSrc), []string{"agent.limits.daily_turns", "node.tz", "agent.limits"})
	if err != nil {
		t.Fatal(err)
	}
	if got["agent.limits.daily_turns"] != 200 {
		t.Fatalf("daily_turns = %#v", got["agent.limits.daily_turns"])
	}
	if _, ok := got["node.tz"]; ok {
		t.Fatal("a missing key has a value")
	}
	if m, ok := got["agent.limits"].(map[string]any); !ok || m["daily_turns"] != 200 {
		t.Fatalf("agent.limits = %#v", got["agent.limits"])
	}
}
