package config

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/bus/natsbus"
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

// Every edge the core joins may take module deliveries: its JWT carries
// the delivery permissions of its own name, and none of another edge's.
func TestJoinedEdgesTakeDeliveries(t *testing.T) {
	n, err := edgeNeeds("home")
	if err != nil {
		t.Fatal(err)
	}
	p, err := natsbus.EdgePermissions("home", n)
	if err != nil {
		t.Fatal(err)
	}
	want := natsbus.DeliveryPermissions("home")
	for _, s := range want.Publish {
		if !slices.Contains(p.Publish, s) {
			t.Errorf("publish %s is missing", s)
		}
	}
	for _, s := range want.Subscribe {
		if !slices.Contains(p.Subscribe, s) {
			t.Errorf("subscribe %s is missing", s)
		}
	}
	for _, s := range append(slices.Clone(p.Publish), p.Subscribe...) {
		if strings.Contains(s, ".garage.") || strings.Contains(s, "_garage") {
			t.Errorf("%s names another edge", s)
		}
	}
	if slices.Contains(p.Subscribe, "_INBOX.>") {
		t.Error("the edge may read every reply in the account")
	}
}
