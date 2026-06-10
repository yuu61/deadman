package tui

import (
	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

// icmpPrivilegeWarnings warns when the config has direct-ICMP or forced next-hop
// targets that cannot probe on this host for lack of socket privilege, and points at
// the fix. In that state the affected probes fail as X with no packet sent (and
// tcpdump shows nothing), so without this hint the cause is invisible. Relay-only
// configs (ssh/snmp/netns/vrf/routeros/tcp) need no local ICMP privilege and stay
// silent.
//
// The two target classes have different requirements, so they are gated
// independently:
//   - direct ICMP works over the raw socket OR the unprivileged datagram socket, so
//     it is broken only when DirectICMPAvailable reports neither is open;
//   - a forced next-hop sends via AF_PACKET and needs CAP_NET_RAW specifically, so it
//     is broken whenever the raw path is unavailable — even if the datagram path is
//     up. Gating it on DirectICMPAvailable would miss exactly that case (datagram up,
//     no CAP_NET_RAW): direct targets work while next-hop targets silently stay X.
//
// The remedy follows from which class is broken. A next-hop needs CAP_NET_RAW, and
// widening net.ipv4.ping_group_range only opens the datagram path — useless to a
// next-hop, and it would flip the direct gate to available and hide this warning — so
// the ping_group_range fix is offered only when the breakage is direct-only.
//
// The hint is split into two short slice entries — a problem line and a fix line that
// front-loads the setcap remedy — because the renderer clips each warning to the
// terminal width (it does not wrap) and fixedHeaderLines counts one line per entry; a
// single ~250-column line would render in the layout math but clip the actionable
// remedy off-screen on any normal terminal.
func icmpPrivilegeWarnings(specs []config.TargetSpec) []string {
	need := localICMPNeeds(specs)

	nexthopBroken := need.nexthop && !rawICMPProbe()
	directBroken := need.direct && !directICMPProbe()

	if !nexthopBroken && !directBroken {
		return nil
	}

	// A broken next-hop (or a mix) is fixed only by CAP_NET_RAW, so never advertise
	// the ping_group_range datagram fix here — it would not help and would silence
	// the warning. setcap/root fixes both classes at once.
	if nexthopBroken {
		return []string{
			"native ICMP unavailable: no CAP_NET_RAW, so probes fail (X) with no packet sent",
			"fix: `sudo setcap cap_net_raw+ep <deadman>`, or run deadman as root " +
				"(a next-hop needs CAP_NET_RAW; ping_group_range does not help it)",
		}
	}

	return []string{
		"native ICMP unavailable: no CAP_NET_RAW and net.ipv4.ping_group_range " +
			"excludes you, so direct probes fail (X) with no packet sent",
		"fix: `sudo setcap cap_net_raw+ep <deadman>`, or widen the range with " +
			`sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`,
	}
}

// icmpNeeds records which local-ICMP target classes a config contains; each has a
// distinct socket-privilege requirement (see icmpPrivilegeWarnings).
type icmpNeeds struct {
	direct  bool // a default direct-ICMP target (raw OR datagram).
	nexthop bool // a forced next-hop target (AF_PACKET, needs CAP_NET_RAW).
}

// localICMPNeeds classifies the targets a config relies on, tracking the direct and
// next-hop classes separately because their privilege needs differ. selectMethod (via
// ping.UsesDirectICMP / ping.UsesNexthop) is the single source of truth per target.
func localICMPNeeds(specs []config.TargetSpec) icmpNeeds {
	var n icmpNeeds

	for _, s := range specs {
		if s.IsSeparator {
			continue
		}

		ps := specToPingSpec(s)
		switch {
		case ping.UsesNexthop(ps):
			n.nexthop = true
		case ping.UsesDirectICMP(ps):
			n.direct = true
		default:
			// Relay modes (ssh/snmp/netns/vrf/routeros/tcp): no local ICMP privilege.
		}
	}

	return n
}

// directICMPProbe reports whether a native direct-ICMP socket (raw or unprivileged
// datagram) can be opened on this host; rawICMPProbe reports whether the raw/
// CAP_NET_RAW path specifically is open, which is what a forced next-hop needs. Both
// are package vars rather than direct calls so tests can pin them and keep startup
// warnings deterministic regardless of the runner's privilege; production leaves them
// as the ping probes.
var (
	directICMPProbe = ping.DirectICMPAvailable
	rawICMPProbe    = ping.RawICMPAvailable
)
