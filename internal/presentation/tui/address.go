package tui

import (
	"github.com/mattn/go-runewidth"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// addressLabel distinguishes hostname probes whose family Compile has fixed. IP
// literals already spell out their version; unconstrained names have no fixed family.
func addressLabel(addr string, plan probe.Plan) string {
	if plan.Destination().Family() != probe.FamilyUnknown {
		return addr
	}

	family := resolveFamilyFor(plan.Params())
	if family == probe.FamilyUnknown {
		return addr
	}

	suffix := " [" + family.String() + "]"

	// Keep the family visible even when the hostname exceeds the ADDRESS width cap.
	return runewidth.Truncate(addr, maxAddressLength-len(suffix), "") + suffix
}

// resolveFamilyFor reads the compiled family of methods supporting resolve_family=.
// It includes the family inferred from a source address, without doing DNS or
// interpreting configuration spellings again.
func resolveFamilyFor(params probe.Params) probe.Family {
	switch p := params.(type) {
	case probe.Direct:
		return p.Family
	case probe.QUIC:
		return p.Family
	case probe.TCP:
		return p.Family
	case probe.SSH:
		return p.Family
	case probe.Netns:
		return p.Family
	case probe.VRF:
		return p.Family
	case probe.Nexthop, probe.SNMP, probe.RouterOS:
		return probe.FamilyUnknown
	default:
		return probe.FamilyUnknown // unreachable: probe.Params is a closed set.
	}
}
