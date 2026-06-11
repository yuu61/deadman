// Package ping abstracts a single ICMP/relay reachability probe.
//
// Each probing mode (direct ICMP, SSH relay, SNMP, network namespace, VRF,
// RouterOS REST API, TCP/hping3, QUIC) implements the Pinger interface. New selects
// an implementation from a Spec based on its "via" and other relay attributes.
package ping

import (
	"context"
	"slices"
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
	MethodQUIC                   // via=quic : QUIC handshake probe.
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
	viaQUIC     = "quic"
)

// labelDirect is the VIA-column label for the native direct-ICMP path. It is also
// the fallback label when a forced nexthop cannot be honored.
const labelDirect = "direct"

// pro-bing network strings that pin the address family during hostname resolution
// (passed to (*probing.Pinger).SetNetwork); "" / "ip" leave the choice to the resolver.
const (
	networkIPv4 = "ip4"
	networkIPv6 = "ip6"
	networkAny  = "ip" // let the resolver choose the family (resolve_family unset).
)

// resolveFamilies maps each accepted resolve_family attribute value to the pro-bing
// network string that pins that address family during hostname resolution. It is the
// single source of truth for the resolve_family contract; extend it to accept more
// spellings.
var resolveFamilies = map[string]string{"ipv4": networkIPv4, "ipv6": networkIPv6}

// resolveNetwork returns the network a target's resolve_family pins ("ip4"/"ip6"), or
// "" to let the resolver choose (today's behavior) when the attribute is unset or
// unrecognized. Only "ipv4"/"ipv6" are recognized; the lenient unknown->"" fallback
// matches deadman's other attribute handling. Honored by the modes that resolve the
// target name locally — direct ICMP and via=quic; the others (relay/snmp/netns/vrf/
// routeros/tcp/nexthop) resolve their family elsewhere (gateway or remote).
func resolveNetwork(s Spec) string { return resolveFamilies[s.Relay["resolve_family"]] }

// selectMethod resolves the probing method from a Spec. New switches on it to
// build the Pinger and Describe to label it, so the two never drift. The
// precedence is: tcp > via=snmp/netns/vrf/routers_api/quic > relay (ssh) > nexthop >
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
	case viaQUIC:
		return MethodQUIC
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
// cannot honor: snmp/routeros originate the probe at the relay, tcp(hping3) has no
// per-probe source, and quic's fresh dial binds [::]:0 — so the attribute is silently
// ignored by those modes (their methodRegistry entry has sourceUnsupported=true). The
// TUI warns about these rather than failing the target, so monitoring still runs with
// the source ignored. The source-capable modes (direct ICMP, nexthop, ssh/netns/vrf)
// return false.
func SourceUnsupported(s Spec) bool {
	if s.Source == "" {
		return false
	}

	return methodRegistry[selectMethod(s)].sourceUnsupported
}

// SourceUnsupportedModeNames returns the sorted short names of the modes that ignore
// source= (quic/routeros/snmp/tcp). The startup warning builds its mode list from this,
// so a new source-ignoring mode shows up in the warning automatically instead of the
// message having to be updated by hand (the drift this PR's review caught).
func SourceUnsupportedModeNames() []string {
	var names []string

	for _, e := range methodRegistry {
		if e.sourceUnsupported {
			names = append(names, e.name)
		}
	}

	slices.Sort(names)

	return names
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
	case MethodQUIC:
		return quicLabel(s)
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

// methodEntry is the per-method dispatch and capability data: how to build the Pinger,
// the short mode name shown in operator warnings, and whether the mode can honor a
// source=. methodRegistry is the single source for all three, so adding a mode (or
// changing its source capability) updates New, SourceUnsupported, and the startup
// warning together instead of in three hand-synced places.
type methodEntry struct {
	build             func(Spec) (Pinger, error)
	name              string // short mode name for the source-ignored warning; "" for source-capable modes.
	sourceUnsupported bool
}

// The subprocess-backed modes share one constructor, parameterized by mode; these
// thin wrappers let methodRegistry hold a uniform func(Spec) (Pinger, error).
func newNetnsPinger(s Spec) (Pinger, error) { return newSubprocessPinger(s, modeNetns) }
func newVRFPinger(s Spec) (Pinger, error)   { return newSubprocessPinger(s, modeVRF) }
func newSSHPinger(s Spec) (Pinger, error)   { return newSubprocessPinger(s, modeSSH) }

// methodRegistry maps each Method to its entry. selectMethod picks the Method (the
// single source of dispatch precedence); New and the source-capability helpers read
// from here. Describe stays a separate switch because its label is Spec-dependent (it
// appends the relay host, tcp port, or gateway).
var methodRegistry = map[Method]methodEntry{
	MethodDirect:   {build: newICMPPinger},
	MethodTCP:      {build: newHPingPinger, name: "tcp", sourceUnsupported: true},
	MethodSNMP:     {build: newSNMPPinger, name: viaSNMP, sourceUnsupported: true},
	MethodNetns:    {build: newNetnsPinger},
	MethodVRF:      {build: newVRFPinger},
	MethodRouterOS: {build: newRouterOSPinger, name: "routeros", sourceUnsupported: true},
	MethodQUIC:     {build: newQUICPinger, name: viaQUIC, sourceUnsupported: true},
	MethodSSH:      {build: newSSHPinger},
	MethodNexthop:  {build: newNexthopPinger},
}

// New builds the Pinger for a Spec, selecting the mode from the relay attributes.
func New(s Spec) (Pinger, error) {
	if s.OSName == "" {
		s.OSName = DefaultOSName()
	}

	if e, ok := methodRegistry[selectMethod(s)]; ok {
		return e.build(s)
	}

	// selectMethod only yields registered methods; direct ICMP is the catch-all for any
	// future method added without a registry entry.
	return newICMPPinger(s)
}
