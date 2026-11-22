//go:build lab

package chatlab

import (
	"testing"

	"github.com/igorrius/ganglion/provider/providertest"
)

func TestWithBuildTakesTheNewestDigest(t *testing.T) {
	old, now := repeat('a', 64), repeat('b', 64)
	req := providertest.Request{Messages: []map[string]any{
		{"role": "tool", "content": `{"build":"` + old + `"}`},
		{"role": "assistant", "content": "next"},
		{"role": "tool", "content": `{"build": "` + now + `","module":"local/x"}`},
	}}
	if got := withBuild(`{"build":"$BUILD"}`, req); got != `{"build":"`+now+`"}` {
		t.Fatalf("args = %s", got)
	}
	if got := withBuild(`{"x":1}`, req); got != `{"x":1}` {
		t.Fatalf("arguments with no token changed: %s", got)
	}
	if got := withBuild(`{"build":"$BUILD"}`, providertest.Request{}); got != `{"build":"$BUILD"}` {
		t.Fatalf("with no digest known the token stays: %s", got)
	}
}

func TestWithBuildOfTakesTheDigestOfTheNamedModule(t *testing.T) {
	a, b := repeat('a', 64), repeat('b', 64)
	req := providertest.Request{Messages: []map[string]any{
		{"role": "user", "content": `[build finished] one: {"build":"` + a + `","module":"local/one","language":"rust"}`},
		{"role": "user", "content": `[build finished] two: {"build":"` + b + `","module":"local/two","language":"rust"}`},
	}}
	got := withBuildOf(`{"builds":["$BUILD:one","$BUILD:two"]}`, req)
	if got != `{"builds":["`+a+`","`+b+`"]}` {
		t.Fatalf("args = %s", got)
	}
	if got := withBuildOf(`{"builds":["$BUILD:three"]}`, req); got != `{"builds":["$BUILD:three"]}` {
		t.Fatalf("a module with no build kept no token: %s", got)
	}
}

func repeat(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
