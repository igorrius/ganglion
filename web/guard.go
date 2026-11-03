package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
)

// blocked are ranges no fetch may reach besides those netip classifies
// (loopback, private, link-local, multicast, unspecified): the "this
// network" block, shared address space (CGNAT), IETF assignments,
// benchmarking and the reserved block with broadcast.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// nat64 is the well-known NAT64 prefix: its addresses carry an IPv4
// address in their last four bytes.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// PublicAddress reports whether a fetch may connect to addr: not
// loopback, private (including IPv6 unique-local fc00::/7), link-local
// (including the cloud metadata address 169.254.169.254), CGNAT,
// multicast, unspecified or reserved. IPv4-mapped and NAT64 addresses are
// judged by the IPv4 address inside (R-WEB-1).
func PublicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.Is6() && nat64.Contains(addr) {
		b := addr.As16()
		addr = netip.AddrFrom4([4]byte(b[12:]))
	}
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// Resolver looks up the addresses of a host name (net.DefaultResolver).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ConnectFunc opens a connection to an address that has been checked,
// "ip:port".
type ConnectFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// dialer resolves a host itself, checks every address it gets, and
// connects only to checked addresses: the name is never resolved again,
// so DNS rebinding cannot swap in a private address, and a redirect to
// another host goes through the same check.
type dialer struct {
	resolve Resolver
	connect ConnectFunc
}

// dial is the transport's DialContext.
func (d dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if !PublicAddress(a) {
			return nil, ErrPrivateAddress
		}
	}
	var errs []error
	for _, a := range addrs {
		conn, err := d.connect(ctx, network, netip.AddrPortFrom(a.Unmap(), parsePort(port)).String())
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (d dialer) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	addrs, err := d.resolve.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("web: %s has no address", host)
	}
	return addrs, nil
}

func parsePort(s string) uint16 {
	n, _ := strconv.ParseUint(s, 10, 16)
	return uint16(n)
}

// directConnect dials with a dialer that checks the address once more
// just before the socket connects.
func directConnect(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Control: checkSocket}
	return d.DialContext(ctx, network, addr)
}

// checkSocket refuses to connect a socket to an address that is not
// public: the last line of defence if a check above were bypassed.
func checkSocket(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !PublicAddress(ap.Addr()) {
		return ErrPrivateAddress
	}
	return nil
}
