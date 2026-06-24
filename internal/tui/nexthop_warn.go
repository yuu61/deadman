package tui

import (
	"net"
	"strings"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

// nexthopWarnings returns operator-facing warnings about next-hop targets that
// would otherwise fail silently:
//   - targets where relay/via/tcp is also set, so the relay mode takes precedence
//     and the gateway is ignored;
//   - targets whose family differs from the gateway's, which cannot be force-routed
//     (IPv4 forcing uses ARP, IPv6 uses NDP) and fail as X;
//   - all next-hop targets when forcing is unsupported on this OS (they fail as X);
//   - strict rp_filter, which can drop the replies to cross-interface forced IPv4
//     probes, making a reachable host look down (there is no IPv6 equivalent knob).
//
// Literal IPv4/IPv6 addresses are classified by family; names (unknown family until
// resolved) are treated as potential forcers and as possibly IPv4 for rp_filter.
func nexthopWarnings(specs []config.TargetSpec) []string {
	c := classifyNexthop(specs)

	var warns []string

	if len(c.modeIgnored) > 0 {
		warns = append(warns,
			"next-hop ignored where relay/via/tcp is set (those modes take precedence): "+
				strings.Join(c.modeIgnored, ", "))
	}

	if len(c.familyMismatch) > 0 {
		warns = append(warns,
			"next-hop and target address families differ (forcing requires the same family); "+
				"these will fail (X): "+strings.Join(c.familyMismatch, ", "))
	}

	if len(c.forced) > 0 && !nexthopForcingSupported() {
		warns = append(warns,
			"next-hop forcing is unsupported on this OS; these targets will fail (X): "+
				strings.Join(c.forced, ", "))
	}

	if c.forcedV4 && nexthopForcingSupported() && rpFilterStrict() {
		warns = append(warns,
			"rp_filter is strict: replies to cross-interface next-hop probes may be dropped "+
				"(set net.ipv4.conf.*.rp_filter to 2/loose or 0/off)")
	}

	return warns
}

// nexthopClasses buckets nexthop targets by how they will actually be treated.
type nexthopClasses struct {
	modeIgnored    []string // a relay mode (relay/via/tcp) takes precedence.
	familyMismatch []string // target and gateway families differ; forcing fails (X).
	forced         []string // genuinely force-routed (IPv4/IPv6 literal, or a name).
	forcedV4       bool     // some forced target is IPv4 or a name (rp_filter applies).
}

// classifyNexthop buckets the nexthop targets in specs. The gateway family is known
// from the literal nexthop=GWIP; the target family from a literal address (a name's
// family is unknown until resolved).
func classifyNexthop(specs []config.TargetSpec) nexthopClasses {
	var c nexthopClasses

	for _, s := range specs {
		if s.Relay["nexthop"] == "" {
			continue
		}

		classifyOne(&c, s)
	}

	return c
}

// classifyOne files one next-hop target (already known to set nexthop=) into c.
func classifyOne(c *nexthopClasses, s config.TargetSpec) {
	// Whether the gateway is honored is ping's precedence call, not ours: a
	// higher-priority mode (relay/via/tcp) wins over nexthop. Delegate to
	// ping.UsesNexthop so this warning can never disagree with the mode New actually
	// builds — e.g. an unknown via= value falls through to nexthop in selectMethod,
	// so it must not be reported here as "ignored".
	if !ping.UsesNexthop(specToPingSpec(s)) {
		c.modeIgnored = append(c.modeIgnored, s.Name)

		return
	}

	gwIP := net.ParseIP(s.Relay["nexthop"])
	target := net.ParseIP(s.Addr) // nil for a name (family unknown until resolved).

	if gwIP != nil && target != nil && (target.To4() == nil) != (gwIP.To4() == nil) {
		c.familyMismatch = append(c.familyMismatch, s.Name)

		return
	}

	c.forced = append(c.forced, s.Name)
	if forcedIsV4(target, gwIP) {
		c.forcedV4 = true
	}
}

// forcedIsV4 reports whether a forced target's probe is IPv4, i.e. rp_filter-
// relevant. A literal target uses its own family; a name is resolved to the
// gateway's family at runtime, so it is IPv4 exactly when the gateway is.
func forcedIsV4(target, gwIP net.IP) bool {
	if target == nil {
		return gwIP != nil && gwIP.To4() != nil
	}

	return target.To4() != nil
}
