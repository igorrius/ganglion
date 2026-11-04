package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// blocked are ranges no fetch may reach besides those netip classifies
// (loopback, private, link-local, multicast, unspecified): the "this
// network" block, shared address space (CGNAT), IETF assignments,
// benchmarking and the reserved block with broadcast.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), // documentation
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),  // documentation
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64: where it leads is the network's choice
}

// Prefixes whose addresses carry an IPv4 address: the well-known NAT64
// prefix in the last four bytes, 6to4 in bytes 2 to 5.
var (
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

// PublicAddress reports whether a fetch may connect to addr: not
// loopback, private (including IPv6 unique-local fc00::/7), link-local
// (including the cloud metadata address 169.254.169.254), CGNAT,
// multicast, unspecified, documentation or reserved. IPv4-mapped, NAT64
// and 6to4 addresses are judged by the IPv4 address inside (R-WEB-1).
func PublicAddress(addr netip.Addr) bool {
	addr = embedded(addr.Unmap())
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

// embedded is the IPv4 address a NAT64 or 6to4 address carries, or addr.
func embedded(addr netip.Addr) netip.Addr {
	b := addr.As16()
	switch {
	case !addr.Is6():
		return addr
	case nat64.Contains(addr):
		return netip.AddrFrom4([4]byte(b[12:]))
	case sixTo4.Contains(addr):
		return netip.AddrFrom4([4]byte(b[2:6]))
	}
	return addr
}

// PublicDial is a DialContext that connects only to public addresses, as
// web.fetch does: every address the name resolves to is checked, and the
// connection goes to a checked address, never to the name again. Other
// clients of the internet use it (module releases, ADR-0041).
func PublicDial(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialer{resolve: net.DefaultResolver, connect: directConnect, timeout: timeout}.dial
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
	timeout time.Duration // per address, so one that never answers leaves time for the next
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
		conn, err := d.connectOne(ctx, network, netip.AddrPortFrom(a.Unmap(), parsePort(port)).String())
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (d dialer) connectOne(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.connect(ctx, network, addr)
}

func (d dialer) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(host, ".")
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if a, ok := legacyIPv4(host); ok {
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

// legacyIPv4 reads the forms of an IPv4 address that inet_aton accepts
// and a system resolver may honour: one to four parts, each decimal,
// octal (leading 0) or hexadecimal (0x), such as 2130706433, 127.1 or
// 0x7f000001. Such a host is an address, never a name to resolve.
func legacyIPv4(host string) (netip.Addr, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 0, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	var v uint64
	for i, n := range nums[:len(nums)-1] {
		if n > 255 {
			return netip.Addr{}, false
		}
		v |= n << (24 - 8*i)
	}
	last := nums[len(nums)-1]
	if last >= 1<<(32-8*(len(nums)-1)) {
		return netip.Addr{}, false
	}
	v |= last
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
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
