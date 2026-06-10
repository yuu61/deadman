package tui

import (
	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

// icmpPrivilegeWarnings warns when the config has direct-ICMP or forced next-hop
// targets but this host can open neither native ICMP path — no CAP_NET_RAW for the
// raw socket and a net.ipv4.ping_group_range that excludes the caller for the
// unprivileged datagram socket. In that state every such probe fails as X with no
// packet sent (and tcpdump shows nothing), so without this hint the cause is
// invisible. Relay-only configs (ssh/snmp/netns/vrf/routeros/tcp) need no local ICMP
// privilege and stay silent.
//
// The hint is split into two short slice entries — a problem line and a fix line
// that front-loads the setcap remedy — because the renderer clips each warning to
// the terminal width (it does not wrap) and fixedHeaderLines counts one line per
// entry; a single ~250-column line would render correctly in the layout math but
// clip the actionable remedy off-screen on any normal terminal.
//
// The gate (DirectICMPAvailable = raw OR datagram) fully covers next-hop only when
// both paths are dead; a host with the datagram path up but no CAP_NET_RAW still
// fails next-hop (it needs raw/AF_PACKET) yet stays silent here. That false negative
// is an accepted limitation, not handled elsewhere: nexthopWarnings checks
// mode/family/OS/rp_filter, never CAP_NET_RAW, so nothing catches the
// datagram-up/raw-down/next-hop case. This warning targets the reported case where
// neither path works.
//
// The remedy is next-hop-aware. A next-hop probe sends via AF_PACKET and needs
// CAP_NET_RAW; widening net.ipv4.ping_group_range only opens the unprivileged
// datagram path, which a next-hop never uses — yet doing so flips directICMPProbe()
// to true and silences this very warning while every next-hop probe stays X. So the
// ping_group_range fix is offered only for a direct-ICMP-only config; when any
// next-hop target is present we steer to setcap/root, which fixes both.
func icmpPrivilegeWarnings(specs []config.TargetSpec) []string {
	if !needsLocalICMP(specs) || directICMPProbe() {
		return nil
	}

	if hasNexthop(specs) {
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

// hasNexthop reports whether any target is force-routed through a next-hop gateway.
// Such targets send via AF_PACKET and need CAP_NET_RAW; the datagram path that
// net.ipv4.ping_group_range governs does not help them, so the warning must not offer
// that remedy when one is present (see icmpPrivilegeWarnings).
func hasNexthop(specs []config.TargetSpec) bool {
	for _, s := range specs {
		if s.IsSeparator {
			continue
		}

		if ping.UsesNexthop(specToPingSpec(s)) {
			return true
		}
	}

	return false
}

// directICMPProbe reports whether a native direct-ICMP socket can be opened on this
// host. It is a package var rather than a direct call so tests can pin the result and
// keep startup warnings deterministic regardless of the runner's CAP_NET_RAW or
// ping_group_range; production leaves it as ping.DirectICMPAvailable.
var directICMPProbe = ping.DirectICMPAvailable

// needsLocalICMP reports whether any target probes through a native socket this
// process opens (direct ICMP or a forced next-hop), which is what depends on local
// CAP_NET_RAW or ping_group_range membership. selectMethod (via ping.UsesLocalICMP)
// is the single source of truth for the per-target verdict.
func needsLocalICMP(specs []config.TargetSpec) bool {
	for _, s := range specs {
		if s.IsSeparator {
			continue
		}

		if ping.UsesLocalICMP(specToPingSpec(s)) {
			return true
		}
	}

	return false
}
