package urlnorm

import "testing"

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://Example.COM/Path?q=1", "https://example.com/Path?q=1", true},
		{"HTTP://example.com", "http://example.com/", true},
		{"https://example.com/a#frag", "https://example.com/a", true},
		{"https://example.com:443/a", "https://example.com/a", true},
		{"http://example.com:80/a", "http://example.com/a", true},
		{"http://example.com:443/a", "http://example.com:443/a", true},
		{"https://example.com:80/a", "https://example.com:80/a", true},
		{"https://example.com:8443/a", "https://example.com:8443/a", true},
		{"https://[::1]:443/a", "https://[::1]/a", true},
		{"https://[::1]:8080/a", "https://[::1]:8080/a", true},
		{"https://example.com/a?x=%2F", "https://example.com/a?x=%2F", true},
		{"ftp://example.com/a", "", false},
		{"javascript:alert(1)", "", false},
		{"//example.com/a", "", false},
		{"/relative", "", false},
		{"https:///nohost", "", false},
		{"", "", false},
		{"not a url", "", false},
	} {
		got, ok := Normalize(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Normalize(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
