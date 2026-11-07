package chatlab

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOnlyTheConnectorKnowsTelegram keeps Telegram behind Peer, so another
// channel means one more connector and the same scenarios.
func TestOnlyTheConnectorKnowsTelegram(t *testing.T) {
	allowed := []string{"telegram.go", "node.go"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || slices.Contains(allowed, f) {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if strings.Contains(imp.Path.Value, "telegram") {
				t.Errorf("%s imports %s", f, imp.Path.Value)
			}
		}
	}
}

// memoryPeer is a channel with no Telegram in it.
type memoryPeer struct {
	said, pressed, replied []string
	sent                   []File
}

func (p *memoryPeer) Say(_ context.Context, text string) error {
	p.said = append(p.said, text)
	return nil
}

func (p *memoryPeer) SendFile(_ context.Context, f File) error {
	p.sent = append(p.sent, f)
	return nil
}

func (p *memoryPeer) Press(_ context.Context, label string) error {
	p.pressed = append(p.pressed, label)
	return nil
}

func (p *memoryPeer) Reply(_ context.Context, to, text string) error {
	p.replied = append(p.replied, to+": "+text)
	return nil
}

func (*memoryPeer) Messages() []Message { return nil }

func TestScenarioStepsNeedNoTelegram(t *testing.T) {
	p := &memoryPeer{}
	ctx := context.Background()
	for _, s := range []Step{{Say: "hi"}, {Press: "Approve"}, {Send: &File{Kind: KindPhoto, Name: "a.jpg"}},
		{Reply: &Reply{To: "apples", Say: "more?"}}} {
		if err := Do(ctx, p, s); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.said) != 1 || len(p.pressed) != 1 || len(p.sent) != 1 || !slices.Equal(p.replied, []string{"apples: more?"}) {
		t.Fatalf("peer %+v", p)
	}
	if err := Do(ctx, p, Step{Judge: "x"}); err == nil {
		t.Fatal("a judge step is not the peer's")
	}
}
