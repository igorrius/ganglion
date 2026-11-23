package config

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/publichttp"
)

// entry is a bus with or without a WebSocket entry.
type entry string

func (e entry) WebSocketAddr() string { return string(e) }

func routeStatus(t *testing.T, e entry, svc *edgejoin.Service, path string) int {
	t.Helper()
	l, err := publichttp.New(publichttp.DefaultConfig("127.0.0.1:0"), slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatal(err)
	}
	mountEdgeJoin(l, svc, e, slog.New(slog.DiscardHandler))
	w := httptest.NewRecorder()
	l.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code
}

// The join routes exist only where /bus does: a core with a chain and the
// bus's WebSocket entry. Anywhere else they are a 404 like any unknown path.
func TestEdgeJoinRoutesFollowTheBusEntry(t *testing.T) {
	svc := edgejoin.New(edgejoin.Config{})
	for _, path := range []string{edgejoin.JoinPath, edgejoin.RenewPath} {
		if got := routeStatus(t, "127.0.0.1:8222", svc, path); got != http.StatusMethodNotAllowed {
			t.Errorf("%s with an entry answered %d, want 405 (the route is there, and takes POST only)", path, got)
		}
		if got := routeStatus(t, "", svc, path); got != http.StatusNotFound {
			t.Errorf("%s with no entry answered %d, want 404", path, got)
		}
		if got := routeStatus(t, "127.0.0.1:8222", nil, path); got != http.StatusNotFound {
			t.Errorf("%s with no service answered %d, want 404", path, got)
		}
	}
}
