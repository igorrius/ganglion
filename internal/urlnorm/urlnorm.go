// Package urlnorm puts web addresses in one form so two spellings of the
// same address compare equal.
package urlnorm

import (
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// defaultPorts are dropped from a normalised address.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

// Normalize returns raw with the scheme and host in lower case, an
// internationalised host in its ASCII (punycode) form, no
// fragment, no default port (80 for http, 443 for https) and "/" for an
// empty path. ok is false for anything but an absolute http or https
// address with a host.
func Normalize(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	def, allowed := defaultPorts[scheme]
	if !allowed || u.Hostname() == "" {
		return "", false
	}
	host, ok := asciiHost(u.Hostname())
	if !ok {
		return "", false
	}
	if port := u.Port(); port != "" && port != def {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]" // a bare IPv6 address
	}
	u.Scheme, u.Host = scheme, host
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" && u.RawPath == "" {
		u.Path = "/"
	}
	return u.String(), true
}

// asciiHost is host in lower case; a host with other than ASCII letters
// is put in its ASCII (punycode) form by the lookup rules of IDNA, so a
// look-alike host shows as what it is and is fetched as shown. ok is
// false for a host IDNA refuses. An ASCII host is left as it is, so names
// IDNA's strict rules refuse (an underscore) still work.
func asciiHost(host string) (string, bool) {
	host = strings.ToLower(host)
	if isASCII(host) {
		return host, true
	}
	a, err := idna.Lookup.ToASCII(host)
	if err != nil || a == "" {
		return "", false
	}
	return a, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
