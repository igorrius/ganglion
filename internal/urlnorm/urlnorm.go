// Package urlnorm puts web addresses in one form so two spellings of the
// same address compare equal.
package urlnorm

import (
	"net"
	"net/url"
	"strings"
)

// defaultPorts are dropped from a normalised address.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

// Normalize returns raw with the scheme and host in lower case, no
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
	host := strings.ToLower(u.Hostname())
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
