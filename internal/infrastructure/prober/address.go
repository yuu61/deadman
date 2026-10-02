package prober

import (
	"context"
	"fmt"
	"net"

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

	ips, err := net.DefaultResolver.LookupNetIP(ctx, network, name)
	if err != nil {
		return "", err
	}

	if len(ips) == 0 {
		return "", fmt.Errorf("no addresses for %s", name)
	}

	return net.JoinHostPort(ips[0].String(), port), nil
}
