package web

import (
	"context"
	"net"
	"testing"
	"time"
)

// PublicDial gives other clients of the internet (module releases) the
// same address check as web.fetch.
func TestPublicDialRefusesLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if c, err := PublicDial(time.Second)(context.Background(), "tcp", ln.Addr().String()); err == nil {
		_ = c.Close()
		t.Fatal("dialled a loopback address")
	}
}
