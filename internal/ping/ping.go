// Package ping abstracts a single ICMP/relay reachability probe.
//
// Each probing mode (direct ICMP, SSH relay, SNMP, network namespace, VRF,
// RouterOS REST API, TCP/hping3) implements the Pinger interface. New selects an
// implementation from a Spec based on its "via" and other relay attributes.
package ping

import (
	"context"
)

// ResultCode classifies a probe outcome. The monitor layer maps each code to its
// result-bar glyph (a success bar, or one of X/t/s).
type ResultCode int

// Probe outcome codes. The concrete values are unobserved outside this package;
// callers always compare against these named constants.
const (
	Success ResultCode = iota
	Failed
	SSHTimeout
	SSHFailed
)

// Result is the outcome of a single probe. RTT is in milliseconds; TTL is -1 when
// unknown (it is captured but not displayed).
type Result struct {
	Success bool
	Code    ResultCode
	RTT     float64
	TTL     int
}

// Pinger sends a single probe. Failures are reported via Result.Code rather than
// an error, so a missing relay binary (e.g. no ssh on Windows) degrades to a
// failure glyph instead of an error path.
type Pinger interface {
	Send(ctx context.Context) Result
}

// Spec describes how to probe one target.
type Spec struct {
	Addr   string
	OSName string // "Linux"/"Darwin"/"FreeBSD"/"Windows"; defaults to host OS.
	Source string
	TCP    string
	Relay  map[string]string
}

// Method identifies the probing mode selected from a Spec. It is the single
// source of truth for dispatch precedence: both New (which Pinger to build) and
// Describe (how the TUI labels the target) derive from selectMethod, so the
// displayed method can never disagree with the one actually used.
type Method int

// Probing methods, in dispatch-precedence order (see selectMethod).
const (
	MethodDirect   Method = iota // direct ICMP via native socket.
	MethodTCP                    // tcp= : hping3 TCP probe.
	MethodSNMP                   // via=snmp.
	MethodNetns                  // via=netns.
	MethodVRF                    // via=vrf.
	MethodRouterOS               // via=routers_api.
	MethodSSH                    // relay= : ssh-wrapped remote ping.
	MethodNexthop                // nexthop= : direct ICMP forced via a gateway.
)

// Relay "via" attribute values that select a probing mode in selectMethod. For
// netns/vrf the value doubles as the `ip` subcommand subprocess.go runs.
const (
	viaSNMP     = "snmp"
	viaNetns    = "netns"
	viaVRF      = "vrf"
	viaRouters  = "routers_api"
	viaRouterOS = "routeros_api"
)

// labelDirect is the VIA-column label for the native direct-ICMP path. It is also
// the fallback label when a forced nexthop cannot be honored.
const labelDirect = "direct"

// pro-bing network strings that pin the address family during hostname resolution
// (passed to (*probing.Pinger).SetNetwork); "" / "ip" leave the choice to the resolver.
const (
	networkIPv4 = "ip4"
	networkIPv6 = "ip6"
)

// resolveFamilies maps each accepted resolve_family attribute value to the pro-bing
// network string that pins that address family during hostname resolution. It is the
// single source of truth for the resolve_family contract; extend it to accept more
// spellings.
var resolveFamilies = map[string]string{"ipv4": networkIPv4, "ipv6": networkIPv6}

// resolveNetwork returns the network a target's resolve_family pins ("ip4"/"ip6"), or
// "" to let the resolver choose (today's behavior) when the attribute is unset or
// unrecognized. Only "ipv4"/"ipv6" are recognized; the lenient unknown->"" fallback
// matches deadman's other attribute handling. Honored only by the direct-ICMP path:
// relay/via/tcp/nexthop targets resolve their family elsewhere (gateway or remote).
func resolveNetwork(s Spec) string { return resolveFamilies[s.Relay["resolve_family"]] }

// selectMethod resolves the probing method from a Spec. New switches on it to
// build the Pinger and Describe to label it, so the two never drift. The
// precedence is: tcp > via=snmp/netns/vrf/routers_api > relay (ssh) > nexthop >
// direct. nexthop is consulted only on the default path, so relay/via/tcp take
// precedence over it.
func selectMethod(s Spec) Method {
	if s.TCP != "" {
		return MethodTCP
	}

	switch s.Relay["via"] {
	case viaSNMP:
		return MethodSNMP
	case viaNetns:
		return MethodNetns
	case viaVRF:
		return MethodVRF
	case viaRouters, viaRouterOS:
		// Both spellings are accepted: the code historically matched "routers_api"
		// while the README documented "routeros_api", so a README-following config
		// silently fell through to the SSH relay. Accepting both is additive and
		// breaks no existing config.
		return MethodRouterOS
	}

	switch {
	case s.Relay["relay"] != "":
		return MethodSSH
	case s.Relay["nexthop"] != "":
		return MethodNexthop
	default:
		return MethodDirect
	}
}

// UsesDirectICMP reports whether a Spec resolves to the default direct-ICMP method,
// which can probe over the privileged raw socket OR the unprivileged datagram socket.
// It shares selectMethod with New/Describe, so it can never disagree with the mode
// actually used. The TUI uses it, alongside UsesNexthop, to decide which missing
// privilege is worth warning about.
func UsesDirectICMP(s Spec) bool {
	return selectMethod(s) == MethodDirect
}

// UsesNexthop reports whether a Spec resolves to the forced next-hop method, which
// sends via AF_PACKET and so needs CAP_NET_RAW specifically — the unprivileged
// datagram path (governed by net.ipv4.ping_group_range) does not apply to it. The
// TUI uses this to avoid steering a next-hop operator toward a ping_group_range fix
// that would not work. It shares selectMethod with New/Describe.
func UsesNexthop(s Spec) bool {
	return selectMethod(s) == MethodNexthop
}

// SourceUnsupported reports whether the Spec sets a source= that its resolved mode
// cannot honor: snmp/routeros originate the probe at the relay and tcp(hping3) has no
// per-probe source, so the attribute is silently ignored by those modes. The TUI
// warns about these (rather than failing the target) so monitoring still runs with
// the source ignored. The source-capable modes (direct ICMP, nexthop, ssh/netns/vrf)
// return false. It shares selectMethod with New, so it cannot disagree with dispatch.
func SourceUnsupported(s Spec) bool {
	if s.Source == "" {
		return false
	}

	m := selectMethod(s)

	return m == MethodSNMP || m == MethodTCP || m == MethodRouterOS
}

// Describe returns a short human label for how a target is probed: the method
// name plus its key differentiator (nexthop gateway, relay host, tcp port). It
// shares selectMethod with New, so the label always matches the mode actually
// used. The TUI shows it in the VIA column.
func Describe(s Spec) string {
	switch selectMethod(s) {
	case MethodTCP:
		return "tcp " + s.TCP
	case MethodSNMP:
		return viaSNMP + " " + s.Relay["relay"]
	case MethodNetns:
		return viaNetns + " " + s.Relay["relay"]
	case MethodVRF:
		return viaVRF + " " + s.Relay["relay"]
	case MethodRouterOS:
		return "routeros " + s.Relay["relay"]
	case MethodSSH:
		return "ssh " + s.Relay["relay"]
	case MethodNexthop:
		return nexthopLabel(s)
	case MethodDirect:
		return labelDirect
	}

	return labelDirect
}

// nexthopLabel renders the VIA label for a forced-nexthop target: the gateway it is
// forced through. Both IPv4 and IPv6 targets are force-routed, so the label always
// names the configured gateway. A mismatched or unreachable next-hop fails as X,
// which the startup check warns about separately.
func nexthopLabel(s Spec) string {
	return "nexthop " + s.Relay["nexthop"]
}

// New builds the Pinger for a Spec, selecting the mode from the relay attributes.
func New(s Spec) (Pinger, error) {
	if s.OSName == "" {
		s.OSName = DefaultOSName()
	}

	switch selectMethod(s) {
	case MethodTCP:
		return newHPingPinger(s)
	case MethodSNMP:
		return newSNMPPinger(s)
	case MethodNetns:
		return newSubprocessPinger(s, modeNetns)
	case MethodVRF:
		return newSubprocessPinger(s, modeVRF)
	case MethodRouterOS:
		return newRouterOSPinger(s)
	case MethodSSH:
		return newSubprocessPinger(s, modeSSH)
	case MethodNexthop:
		return newNexthopPinger(s)
	case MethodDirect:
		return newICMPPinger(s)
	}

	// selectMethod only yields the methods handled above; direct ICMP is the
	// catch-all for any future method added without its own New branch.
	return newICMPPinger(s)
}
