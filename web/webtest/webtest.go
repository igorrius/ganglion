// Package webtest is a fake internet for tests of package web: host names
// resolve to the addresses a test gives, and a connection to an address
// reaches the local server registered for it. The fetcher's address
// checks run on the fake addresses, so a test can call a "public" site
// that is really a local server, and see a private address refused.
package webtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// Net is a fake internet.
type Net struct {
	mu      sync.Mutex
	hosts   map[string][]netip.Addr
	servers map[netip.Addr]string // address → local listener "127.0.0.1:port"
	dialed  []string              // addresses connected to, "ip:port"
}

// New returns an empty fake internet.
func New() *Net {
	return &Net{hosts: map[string][]netip.Addr{}, servers: map[netip.Addr]string{}}
}

// Site serves h at host, which resolves to addr (a public-looking
// address such as 93.184.216.10). It returns the site's base URL,
// "http://host".
func (n *Net) Site(t testing.TB, host, addr string, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ip := netip.MustParseAddr(addr)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hosts[strings.ToLower(host)] = append(n.hosts[strings.ToLower(host)], ip)
	n.servers[ip] = srv.Listener.Addr().String()
	return "http://" + host
}

// Resolve makes host resolve to addrs without serving anything there:
// the fetcher must refuse before it connects.
func (n *Net) Resolve(host string, addrs ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range addrs {
		n.hosts[strings.ToLower(host)] = append(n.hosts[strings.ToLower(host)], netip.MustParseAddr(a))
	}
}

// LookupNetIP resolves a host name of the fake internet (web.Resolver).
func (n *Net) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	addrs, ok := n.hosts[strings.ToLower(host)]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

// Connect dials the local server registered for addr, an "ip:port" the
// fetcher has already checked (web.ConnectFunc).
func (n *Net) Connect(ctx context.Context, network, addr string) (net.Conn, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.dialed = append(n.dialed, addr)
	local, ok := n.servers[ap.Addr()]
	n.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("webtest: nothing listens at %s: %w", addr, errors.ErrUnsupported)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, local)
}

// Dialed lists the addresses the fetcher connected to.
func (n *Net) Dialed() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.dialed...)
}

// Reply is a handler that answers with a content type and a body.
func Reply(contentType, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	})
}

// Redirect is a handler that redirects every request to target.
func Redirect(target string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// NBURates is a reply in the shape of the National Bank of Ukraine's
// exchange rate API (bank.gov.ua/NBUStatService/v1/statdirectory/exchange?json).
const NBURates = `[{"r030":840,"txt":"Долар США","rate":41.4529,"cc":"USD","exchangedate":"03.11.2026"},` +
	`{"r030":978,"txt":"Євро","rate":48.1023,"cc":"EUR","exchangedate":"03.11.2026"}]`
