// Package probe defines what a single reachability probe is, independent of how it is
// sent: the outcome of one probe (Result and its ResultCode), the Pinger port every
// probing mode implements, and the probe a target's attributes describe — a raw Spec,
// which Compile validates into a Plan: the selected Method and that method's parameters
// with every default filled in.
//
// The concrete probing modes (direct ICMP, SSH relay, SNMP, network namespace, VRF,
// RouterOS REST API, TCP/hping3, QUIC, forced next-hop) live in the infrastructure
// layer and are built from a Plan there. This package does no I/O.
package probe

import "context"

// ResultCode classifies a probe outcome. The presentation layer maps each code to its
// result-bar glyph (a success bar, or one of X/t/s/?).
type ResultCode int

// Probe outcome codes. The concrete values are unobserved outside this package;
// callers always compare against these named constants. Failed means a probe was
// attempted but the target did not answer. Unavailable and the relay codes mean the
// target's reachability could not be determined.
const (
	Failed ResultCode = iota
	Success
	RelayTimeout
	RelayFailed
	Unavailable
)

// Result is the outcome of a single probe. RTT is in milliseconds, and only a success has
// one.
type Result struct {
	Code ResultCode
	RTT  float64
}

// FailedResult reports that a probe was attempted but got no target response.
func FailedResult() Result {
	return Result{Code: Failed}
}

// UnavailableResult reports that the probe could not determine target reachability.
func UnavailableResult() Result {
	return Result{Code: Unavailable}
}

// RelayTimeoutResult reports that the relay did not answer in time.
func RelayTimeoutResult() Result {
	return Result{Code: RelayTimeout}
}

// RelayFailedResult reports that the relay could not be used or refused the probe.
func RelayFailedResult() Result {
	return Result{Code: RelayFailed}
}

// SuccessResult returns a successful probe Result taking rtt milliseconds.
func SuccessResult(rtt float64) Result {
	return Result{Code: Success, RTT: rtt}
}

// IsSuccess is the sole success predicate: every other result — the zero value and
// unknown codes included — is a failure. The result bar draws a failure as X/t/s/?.
func (r Result) IsSuccess() bool { return r.Code == Success }

// IsObserved reports whether the result says anything about the target itself.
// An unavailable probe must not enter packet-loss statistics.
func (r Result) IsObserved() bool { return r.Code == Success || r.Code == Failed }

// Pinger sends a single probe. Outcomes are reported via Result.Code; a missing
// relay binary is Unavailable rather than a target failure.
type Pinger interface {
	Send(ctx context.Context) Result
}

// Spec describes a probe using typed parameters, whose type selects the method. Compile
// validates the parameters and fills defaults before adapters or identities can observe
// them.
type Spec struct {
	Addr   string
	Params Params
}
