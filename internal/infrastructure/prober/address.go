package prober

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Resolve one address before measuring RTT. Dialing a literal prevents fallback
// to another address from hiding a failed path or adding DNS time to the result.
func resolveProbeAddress(
	ctx context.Context,
	dest probe.Destination,
	port, network string,
) (string, error) {
	if addr, ok := dest.IP(); ok {
		return net.JoinHostPort(addr.String(), port), nil
	}

	name, _ := dest.Name()

	addr, err := lookupFirst(ctx, network, name)
	if err != nil {
		return "", err
	}

	return net.JoinHostPort(addr.String(), port), nil
}

// lookupFirst resolves name in network (networkAny, networkIPv4 or networkIPv6) and
// returns the resolver's first address, as iputils' ping takes getaddrinfo's first. The
// resolver orders the answers for this host (RFC 6724), so every method — and direct on
// every OS — probes the same address of a name rather than each preferring a family of
// its own.
func lookupFirst(ctx context.Context, network, name string) (netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, network, name)
	if err != nil {
		return netip.Addr{}, err
	}

	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("no addresses for %s", name)
	}

	// An IPv4 answer may come back mapped into IPv6; it is probed as the IPv4 it is.
	return addrs[0].Unmap(), nil
}
