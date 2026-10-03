package fetch

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
)

// ErrForbiddenTarget is returned when a connection would reach a
// non-public address. It is wrapped in *Error with KindForbidden.
var ErrForbiddenTarget = errors.New("destination address is not allowed")

// deniedPrefixes are ranges never reachable from the fetcher, beyond what
// netip.Addr's IsPrivate/IsLoopback/... helpers already cover. Transition
// mechanisms that embed IPv4 addresses (NAT64, 6to4, Teredo) are denied
// outright because they could tunnel to a private IPv4 target.
var deniedPrefixes = mustPrefixes(
	"0.0.0.0/8",          // "this network"
	"100.64.0.0/10",      // carrier-grade NAT
	"169.254.0.0/16",     // link-local, incl. EC2/ECS metadata 169.254.169.254 / 169.254.170.2
	"192.0.0.0/24",       // IETF protocol assignments
	"192.0.2.0/24",       // TEST-NET-1
	"198.18.0.0/15",      // benchmarking
	"198.51.100.0/24",    // TEST-NET-2
	"203.0.113.0/24",     // TEST-NET-3
	"240.0.0.0/4",        // reserved
	"255.255.255.255/32", // broadcast
	"64:ff9b::/96",       // NAT64
	"64:ff9b:1::/48",     // local-use NAT64
	"100::/64",           // discard-only
	"2001::/32",          // Teredo
	"2001:db8::/32",      // documentation
	"2002::/16",          // 6to4
	"fec0::/10",          // deprecated site-local
	"fd00:ec2::254/128",  // EC2 IPv6 metadata (also inside fc00::/7)
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// Guard decides which resolved addresses the fetcher may connect to.
type Guard struct {
	// AllowLoopback permits 127.0.0.0/8 and ::1 on any port. Local dev only
	// (the demo store runs on localhost). Never enable in AWS: the Lambda
	// runtime API listens on loopback.
	AllowLoopback bool
}

// CheckAddr reports whether ip:port is an acceptable destination.
func (g Guard) CheckAddr(ip netip.Addr, port int) error {
	ip = ip.Unmap() // ::ffff:127.0.0.1 must be judged as 127.0.0.1
	if g.AllowLoopback && ip.IsLoopback() {
		return nil
	}
	if port != 80 && port != 443 {
		return fmt.Errorf("%w: port %d", ErrForbiddenTarget, port)
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return fmt.Errorf("%w: %s", ErrForbiddenTarget, ip)
	}
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s", ErrForbiddenTarget, ip)
		}
	}
	return nil
}

// Control is a net.Dialer Control hook. It runs after DNS resolution, for
// every connection attempt (including each redirect hop and each Happy
// Eyeballs attempt), with the literal IP about to be dialed. Checking here
// rather than before resolution defeats DNS rebinding.
func (g Guard) Control(network, address string, _ syscall.RawConn) error {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrForbiddenTarget, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", ErrForbiddenTarget, host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("%w: bad port %q", ErrForbiddenTarget, portStr)
	}
	return g.CheckAddr(ip, port)
}
