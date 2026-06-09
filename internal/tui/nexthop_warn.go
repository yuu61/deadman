package tui

import (
	"net"
	"strings"

	"github.com/yuu61/deadman/internal/config"
)

// nexthopWarnings returns operator-facing warnings about next-hop targets that
// would otherwise fail silently:
//   - targets where relay/via/tcp is also set, so the relay mode takes precedence
//     and the gateway is ignored;
//   - IPv6 targets, where next-hop forcing is unimplemented and the probe falls
//     back to ordinary routing (so the gateway is ignored);
//   - all next-hop targets when forcing is unsupported on this OS (they fail as X);
//   - strict rp_filter, which can drop the replies to cross-interface forced
//     probes, making a reachable host look down.
//
// Literal IPv4/IPv6 addresses are classified by family; names (unknown family
// until resolved) are treated as potential forcers.
func nexthopWarnings(specs []config.TargetSpec) []string {
	c := classifyNexthop(specs)

	var warns []string

	if len(c.modeIgnored) > 0 {
		warns = append(warns,
			"next-hop ignored where relay/via/tcp is set (those modes take precedence): "+
				strings.Join(c.modeIgnored, ", "))
	}

	if len(c.ipv6Ignored) > 0 {
		warns = append(warns,
			"next-hop ignored for IPv6 targets (probed via normal routing): "+
				strings.Join(c.ipv6Ignored, ", "))
	}

	if len(c.forced) > 0 && !nexthopForcingSupported() {
		warns = append(warns,
			"next-hop forcing is unsupported on this OS; these targets will fail (X): "+
				strings.Join(c.forced, ", "))
	}

	if len(c.forced) > 0 && nexthopForcingSupported() && rpFilterStrict() {
		warns = append(warns,
			"rp_filter is strict: replies to cross-interface next-hop probes may be dropped "+
				"(set net.ipv4.conf.*.rp_filter to 2/loose or 0/off)")
	}

	return warns
}

// nexthopClasses buckets nexthop targets by how they will actually be treated.
type nexthopClasses struct {
	modeIgnored []string // a relay mode (relay/via/tcp) takes precedence.
	ipv6Ignored []string // IPv6: forcing unimplemented, probed via normal routing.
	forced      []string // IPv4 literal or unresolved name: genuinely force-routed.
}

// classifyNexthop buckets the nexthop targets in specs.
func classifyNexthop(specs []config.TargetSpec) nexthopClasses {
	var c nexthopClasses

	for _, s := range specs {
		if s.Relay["nexthop"] == "" {
			continue
		}

		switch ip := net.ParseIP(s.Addr); {
		case s.Relay["relay"] != "" || s.Relay["via"] != "" || s.TCP != "":
			c.modeIgnored = append(c.modeIgnored, s.Name)
		case ip != nil && ip.To4() == nil:
			c.ipv6Ignored = append(c.ipv6Ignored, s.Name)
		default:
			c.forced = append(c.forced, s.Name)
		}
	}

	return c
}
