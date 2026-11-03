package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/web"
	"github.com/igorrius/ganglion/web/webtest"
)

// Every fetch is counted by its result, the error's kind, never the
// address; the size of a body read is a histogram.
func TestFetchMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	cfg := web.DefaultFetchConfig()
	cfg.MaxBytes = 1024
	f, n, _ := newFetcher(t, cfg)
	fits := n.Site(t, "fits.example", "93.184.216.11", webtest.Reply("text/plain", strings.Repeat("a", 1000)))
	big := n.Site(t, "big.example", "93.184.216.10", webtest.Reply("text/plain", strings.Repeat("a", 1025)))
	for _, u := range []string{fits, big, "http://127.0.0.1/", "ftp://example.com/"} {
		_, _ = f.Fetch(context.Background(), u, web.AnyHost)
	}
	for result, want := range map[string]float64{"ok": 1, "too_large": 1, "private_address": 1, "not_allowed": 1} {
		if got := rec.Value(t, "ganglion.web.fetches", map[string]string{"result": result}); got != want {
			t.Errorf("fetches with result %s = %v, want %v", result, got, want)
		}
	}
	if got := rec.Sum(t, "ganglion.web.fetch.bytes", nil); got != 1000 {
		t.Errorf("fetched bytes sum to %v, want 1000", got)
	}
}
