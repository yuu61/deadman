// Package ping abstracts a single ICMP/relay reachability probe.
//
// Each probing mode (direct ICMP, SSH relay, SNMP, network namespace, VRF,
// RouterOS REST API, TCP/hping3) implements the Pinger interface. New selects an
// implementation from a Spec based on its "via" and other relay attributes.
package ping

import "context"

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

// New builds the Pinger for a Spec, selecting the mode from the relay attributes.
func New(s Spec) (Pinger, error) {
	if s.OSName == "" {
		s.OSName = DefaultOSName()
	}

	if s.TCP != "" {
		return newHPingPinger(s)
	}

	switch s.Relay["via"] {
	case "snmp":
		return newSNMPPinger(s)
	case "netns":
		return newSubprocessPinger(s, modeNetns)
	case "vrf":
		return newSubprocessPinger(s, modeVRF)
	case "routers_api":
		return newRouterOSPinger(s)
	}

	if s.Relay["relay"] != "" {
		return newSubprocessPinger(s, modeSSH)
	}

	return newICMPPinger(s)
}
