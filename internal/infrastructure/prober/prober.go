// Package prober implements the probing modes behind the probe.Pinger port: native ICMP,
// SSH, SNMP, network namespace, VRF, RouterOS REST, TCP, QUIC and next-hop. Host
// reports which of them this host can send, by the same checks the modes use.
package prober

import (
	"fmt"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// pro-bing / LookupNetIP network strings that pin the address family during hostname
// resolution; "" (for pro-bing) or "ip" (for LookupNetIP) lets the resolver choose.
const (
	networkIPv4 = "ip4"
	networkIPv6 = "ip6"
	networkAny  = "ip"
)

// usPerMs converts microseconds to milliseconds.
const usPerMs = 1000.0

var familyNetworks = map[probe.Family]string{
	probe.FamilyIPv4: networkIPv4,
	probe.FamilyIPv6: networkIPv6,
}

func resolveNetwork(f probe.Family) string { return familyNetworks[f] }

// New builds a pinger from the resolved parameter type. Compile already validated it.
// row is the ID of the row it probes for, stable across reloads and restarts and distinct
// among duplicate rows; the snmp method names the agent's test entry by it.
func New(plan probe.Plan, row string) (probe.Pinger, error) {
	dest := plan.Destination()
	switch p := plan.Params().(type) {
	case probe.LocalParams:
		return newLocalPinger(dest, p)
	case probe.RelayParams:
		return newRelayPinger(dest, p, row)
	default:
		return nil, fmt.Errorf("invalid probe plan: %T parameters", p)
	}
}

func newLocalPinger(dest probe.Destination, params probe.LocalParams) (probe.Pinger, error) {
	switch p := params.(type) {
	case probe.Direct:
		return newICMPPinger(dest, p)
	case probe.TCP:
		return newTCPPinger(dest, p), nil
	case probe.QUIC:
		return newQUICPinger(dest, p)
	case probe.Nexthop:
		return newNexthopPinger(dest, p)
	default:
		return nil, fmt.Errorf("invalid local probe parameters: %T", p)
	}
}

// Check reports the build errors New returns for plan that need no I/O and hold on every
// OS: a value the relay's argv would carry as a bare operand that begins with '-'. --check
// runs it after probe.Compile, so a line monitoring would refuse to build fails the check
// too. What this OS cannot send stays New's alone: a configuration may be checked on
// another host than the one that monitors with it.
func Check(plan probe.Plan) error {
	relay, ok := plan.Params().(probe.RelayParams)
	if !ok {
		return nil
	}

	return argvOperandError(plan.Destination(), relay)
}

func newRelayPinger(
	dest probe.Destination,
	params probe.RelayParams,
	row string,
) (probe.Pinger, error) {
	err := argvOperandError(dest, params)
	if err != nil {
		return nil, err
	}

	switch p := params.(type) {
	case probe.SNMP:
		return newSNMPPinger(dest, p, row)
	case probe.Netns:
		return newNetnsPinger(dest, p)
	case probe.VRF:
		return newVRFPinger(dest, p)
	case probe.RouterOS:
		return newRouterOSPinger(dest, p)
	case probe.SSH:
		return newSSHPinger(dest, p)
	default:
		return nil, fmt.Errorf("invalid relay probe parameters: %T", p)
	}
}
