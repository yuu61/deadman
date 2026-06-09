// Package ping abstracts a single ICMP/relay reachability probe.
//
// Each probing mode (direct ICMP, SSH relay, SNMP, network namespace, VRF,
// RouterOS REST API, TCP/hping3) implements the Pinger interface. New selects an
// implementation from a Spec based on its "via" and other relay attributes.
package ping

import (
	"context"
	"net"
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
	case "snmp":
		return MethodSNMP
	case "netns":
		return MethodNetns
	case "vrf":
		return MethodVRF
	case "routers_api", "routeros_api":
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

// Describe returns a short human label for how a target is probed: the method
// name plus its key differentiator (nexthop gateway, relay host, tcp port). It
// shares selectMethod with New, so the label always matches the mode actually
// used. The TUI shows it in the VIA column.
func Describe(s Spec) string {
	switch selectMethod(s) {
	case MethodTCP:
		return "tcp " + s.TCP
	case MethodSNMP:
		return "snmp " + s.Relay["relay"]
	case MethodNetns:
		return "netns " + s.Relay["relay"]
	case MethodVRF:
		return "vrf " + s.Relay["relay"]
	case MethodRouterOS:
		return "routeros " + s.Relay["relay"]
	case MethodSSH:
		return "ssh " + s.Relay["relay"]
	case MethodNexthop:
		// A literal IPv6 target cannot be force-routed (the link transport is
		// IPv4-only), so nexthopPinger.Send falls back to ordinary routing. Report
		// the path actually taken rather than a gateway that is ignored; the startup
		// check warns about this separately. A hostname's family is unknown until
		// resolved, so it keeps the forced label (matching the probe's intent).
		if ip := net.ParseIP(s.Addr); ip != nil && ip.To4() == nil {
			return "direct"
		}

		return "nexthop " + s.Relay["nexthop"]
	default:
		return "direct"
	}
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
	default:
		return newICMPPinger(s)
	}
}
