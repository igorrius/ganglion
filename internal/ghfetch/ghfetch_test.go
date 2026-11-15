package ghfetch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testClient(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/small":
			_, _ = w.Write([]byte("hello"))
		case "/big":
			_, _ = w.Write(bytes.Repeat([]byte("x"), 100))
		case "/redirect":
			http.Redirect(w, r, "http://evil.example/x", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return New(WithHosts(u.Hostname()), WithTransport(srv.Client().Transport)), srv
}

func TestCopyStreamsAndBoundsTheBody(t *testing.T) {
	c, srv := testClient(t)
	var buf bytes.Buffer
	n, err := c.Copy(context.Background(), srv.URL+"/small", &buf, 10)
	if err != nil || n != 5 || buf.String() != "hello" {
		t.Fatalf("copy: %d %q %v", n, buf.String(), err)
	}
	buf.Reset()
	if _, err := c.Copy(context.Background(), srv.URL+"/big", &buf, 50); err == nil || !strings.Contains(err.Error(), "over 50 bytes") {
		t.Fatalf("over the limit: %v", err)
	}
	if buf.Len() > 51 {
		t.Fatalf("wrote %d bytes past the limit", buf.Len())
	}
	if _, err := c.Get(context.Background(), srv.URL+"/big", 99); err == nil {
		t.Fatal("Get over the limit succeeded")
	}
	if _, err := c.Get(context.Background(), srv.URL+"/missing", 99); err == nil {
		t.Fatal("a 404 succeeded")
	}
}

func TestRedirectToAnotherHostFails(t *testing.T) {
	c, srv := testClient(t)
	var buf bytes.Buffer
	if _, err := c.Copy(context.Background(), srv.URL+"/redirect", &buf, 10); err == nil || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("redirect off the hosts: %v", err)
	}
}

func TestPlainHTTPFailsWithoutTheTestTransport(t *testing.T) {
	c := New(WithHosts("127.0.0.1"))
	for _, u := range []string{"http://127.0.0.1/x", "https://127.0.0.1:8443/x", "https://other.example/x"} {
		if _, err := c.Get(context.Background(), u, 10); err == nil {
			t.Fatalf("%s was fetched", u)
		}
	}
}
