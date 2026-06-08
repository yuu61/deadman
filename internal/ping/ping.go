// Package ping abstracts a single ICMP/relay reachability probe.
//
// Each probing mode (direct ICMP, SSH relay, SNMP, network namespace, VRF,
// RouterOS REST API, TCP/hping3) implements the Pinger interface. New selects an
// implementation from a Spec the same way the original Python Ping class branched
// on its "via" attribute.
package ping

import "context"

// ResultCode classifies a probe outcome. The values match the original numeric
// constants so the result glyphs (X/t/s) map cleanly.
type ResultCode int

// Probe outcome codes. The values match the original numeric constants so the
// result glyphs (X/t/s) map cleanly.
const (
	Success    ResultCode = 0
	Failed     ResultCode = -1
	SSHTimeout ResultCode = -2
	SSHFailed  ResultCode = -3
)

// Result is the outcome of a single probe. RTT is in milliseconds; TTL is -1 when
// unknown (it is captured but, as in the original, never displayed).
type Result struct {
	Success bool
	Code    ResultCode
	RTT     float64
	TTL     int
}

// Pinger sends a single probe. Failures are reported via Result.Code rather than
// an error, mirroring the original design.
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
