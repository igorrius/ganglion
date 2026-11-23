package config

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/publichttp"
)

// A bus with no WebSocket entry leaves /bus unrouted: a 404 like any
// unknown path, never a tunnel to something else (ADR-0114).
func TestBusRouteIsAbsentWithoutAnEntry(t *testing.T) {
	l, err := publichttp.New(publichttp.DefaultConfig("127.0.0.1:0"), slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := mountBus(l, &natsbus.Bus{}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/bus", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	l.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("/bus answered %d with no entry", w.Code)
	}
}
