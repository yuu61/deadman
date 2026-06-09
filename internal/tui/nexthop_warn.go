package tui

import (
	"net"
	"strings"

	"github.com/yuu61/deadman/internal/config"
)

// nexthopWarnings returns operator-facing warnings about next-hop targets that
// would otherwise fail silently:
//   - IPv6 targets, where next-hop forcing is unimplemented and the probe falls
//     back to ordinary routing (so the configured gateway is ignored);
//   - strict rp_filter, which can drop the replies to cross-interface forced
//     probes, making a reachable host look down.
//
// Only literal IPv6 addresses are flagged (names are not resolved at startup).
func nexthopWarnings(specs []config.TargetSpec) []string {
	var (
		ipv6Ignored []string
		hasNexthop  bool
	)

	for _, s := range specs {
		if s.Relay["nexthop"] == "" {
			continue
		}

		hasNexthop = true

		if ip := net.ParseIP(s.Addr); ip != nil && ip.To4() == nil {
			ipv6Ignored = append(ipv6Ignored, s.Name)
		}
	}

	var warns []string

	if len(ipv6Ignored) > 0 {
		warns = append(
			warns,
			"next-hop ignored for IPv6 targets (probed via normal routing): "+strings.Join(
				ipv6Ignored,
				", ",
			),
		)
	}

	if hasNexthop && rpFilterStrict() {
		warns = append(warns,
			"rp_filter is strict: replies to cross-interface next-hop probes may be dropped "+
				"(set net.ipv4.conf.*.rp_filter to 2/loose or 0/off)")
	}

	return warns
}
