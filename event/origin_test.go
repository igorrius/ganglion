package event

import (
	"context"
	"testing"
)

// R-DLV-14: a context carries the function, and the origin is read when asked.
func TestOriginOf(t *testing.T) {
	if _, ok := OriginOf(context.Background()); ok {
		t.Fatal("a bare context carries an origin")
	}
	o := Origin{Kind: OriginTurn, Session: "s1"}
	ctx := WithOrigin(context.Background(), func() Origin { return o })
	got, ok := OriginOf(ctx)
	if !ok || got != o {
		t.Fatalf("origin %+v, %v", got, ok)
	}
	o.Tainted = true
	if got, _ := OriginOf(ctx); !got.Tainted {
		t.Errorf("origin %+v: the change after WithOrigin is not seen", got)
	}
}
