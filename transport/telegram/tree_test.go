package telegram

import (
	"strings"
	"testing"
)

// nbsp stands for a space a line of a tree needs kept: Telegram and Markdown trim
// an ordinary one at the start of a line.
const nbsp = "\u00a0"

func TestRenderKeepsTreeLevels(t *testing.T) {
	text, ents := render("root\n├── a\n│   └── b\n    └── c\n└── d")
	want := "root\n├── a\n│" + strings.Repeat(nbsp, 3) + "└── b\n" + strings.Repeat(nbsp, 4) + "└── c\n└── d"
	if text != want {
		t.Fatalf("text %q, want %q", text, want)
	}
	if len(ents) != 0 {
		t.Fatalf("entities %+v, want plain text", ents)
	}
}

// The owner's report: a tree of links stays links, with its levels.
func TestRenderTreeKeepsLinks(t *testing.T) {
	src := "[PE-1](https://x.org/1) — Support (Initiative)\n└── [PE-2](https://x.org/2) — Support (Epic)\n" +
		"    └── [PE-3](https://x.org/3) — Reduce time\n        Статус: In Test"
	text, ents := render(src)
	want := "PE-1 — Support (Initiative)\n└── PE-2 — Support (Epic)\n" + strings.Repeat(nbsp, 4) + "└── PE-3 — Reduce time\n" +
		strings.Repeat(nbsp, 8) + "Статус: In Test"
	if text != want {
		t.Fatalf("text %q, want %q", text, want)
	}
	links := 0
	for _, e := range ents {
		if e.Type == entityPre {
			t.Fatal("the tree came out preformatted")
		}
		if e.Type == "text_link" {
			links++
		}
	}
	if links != 3 {
		t.Fatalf("%d links, want 3: %+v", links, ents)
	}
}

func TestRenderRichTreeKeepsLinks(t *testing.T) {
	blocks := renderRich("[PE-1](https://x.org/1)\n└── [PE-2](https://x.org/2)\n    └── [PE-3](https://x.org/3)").Blocks
	if len(blocks) != 1 || blocks[0].Type != richParagraph {
		t.Fatalf("blocks %+v", blocks)
	}
	got := richJSON(t, "a\n    └── [b](https://x.org)")
	if !strings.Contains(got, `"a\n`+strings.Repeat(nbsp, 4)+`└── "`) || !strings.Contains(got, `"type":"url"`) {
		t.Fatalf("got %s", got)
	}
}

func TestRenderTreeBesideText(t *testing.T) {
	text, ents := render("Вот дерево:\n\nroot\n└── a\n\nи всё")
	if want := "Вот дерево:\n\nroot\n└── a\n\nи всё"; text != want {
		t.Fatalf("text %q, want %q", text, want)
	}
	if len(ents) != 0 {
		t.Fatalf("entities %+v", ents)
	}
}

func TestRenderTreeInAList(t *testing.T) {
	text, _ := render("- item\n\n  root\n  ├── a\n  │   └── b")
	if !strings.Contains(text, "├── a") || !strings.Contains(text, "│"+strings.Repeat(nbsp, 3)+"└── b") {
		t.Fatalf("text %q loses the levels", text)
	}
	if strings.Contains(text, nbsp+"├") {
		t.Fatalf("text %q keeps the item's own indentation", text)
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
			if text, _ := render(tc.md + "\n   more"); strings.Contains(text, nbsp) {
				t.Fatalf("%q was taken for a tree: %q", tc.md, text)
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
