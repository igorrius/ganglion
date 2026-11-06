package telegram

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestRenderKeepsTreeLevels(t *testing.T) {
	src := "root\n├── a\n│   └── b\n    └── c\n└── d"
	text, ents := render(src)
	if text != src {
		t.Fatalf("text %q, want the source", text)
	}
	want := []entity{ent(entityPre, 0, len(utf16.Encode([]rune(src))))}
	if !slices.Equal(ents, want) {
		t.Fatalf("entities %+v, want %+v", ents, want)
	}
}

func TestRenderTreeBesideText(t *testing.T) {
	text, ents := render("Вот дерево:\n\n" + "root\n└── a\n\nи всё")
	if want := "Вот дерево:\n\nroot\n└── a\n\nи всё"; text != want {
		t.Fatalf("text %q, want %q", text, want)
	}
	if len(ents) != 1 || ents[0].Type != entityPre || ents[0].Offset != 13 {
		t.Fatalf("entities %+v", ents)
	}
}

func TestRenderTreeInAList(t *testing.T) {
	text, ents := render("- item\n\n  root\n  ├── a\n  │   └── b")
	if !strings.Contains(text, "root\n├── a\n│   └── b") {
		t.Fatalf("text %q keeps the item's indentation or loses the levels", text)
	}
	if len(ents) != 1 || ents[0].Type != entityPre {
		t.Fatalf("entities %+v", ents)
	}
}

func TestRenderNotATree(t *testing.T) {
	for _, tc := range []struct{ name, md string }{
		{"mid-line symbol", "итог └ готово\nещё строка"},
		{"a plan's step", "✅ 1. собрать └ всё\n⏳ 2. отправить ├ ответ"},
		{"a bar alone", "│ a\n│ b"},
		{"a heading", "# └ a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ents := render(tc.md)
			for _, e := range ents {
				if e.Type == entityPre {
					t.Fatalf("%q came out preformatted", tc.md)
				}
			}
		})
	}
}

func TestRenderTreeInFenceUnchanged(t *testing.T) {
	text, ents := render("```\nroot\n  └── a\n```")
	if text != "root\n  └── a" {
		t.Fatalf("text %q", text)
	}
	if len(ents) != 1 || ents[0].Type != entityPre {
		t.Fatalf("entities %+v", ents)
	}
}

func TestIsTreeLine(t *testing.T) {
	for line, want := range map[string]bool{
		"├── a": true, "└ a": true, "    └── a": true, "│   ├── a": true, " │ │ └ a": true,
		"│ a": false, "a └ b": false, "": false, "- └ a": false,
	} {
		if got := isTreeLine(line); got != want {
			t.Errorf("isTreeLine(%q) = %v, want %v", line, got, want)
		}
	}
}
