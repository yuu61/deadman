package monitoring

import "github.com/yuu61/deadman/internal/domain/probe"

// ProbeWarning is the Diagnostic kinds about how the targets will be probed: this host's
// ICMP privilege and reverse-path filtering, and the deprecated snmp method. probeWarnings
// selects them from the plans of the rows that will be probed. Each kind and the rule
// that selects it are defined below.
//
//sumtype:decl
type ProbeWarning interface {
	Diagnostic
	probeWarning()
}

// probeTarget is a row that will be probed, as the probing warnings see it: its name and
// the plan it is probed by.
type probeTarget struct {
	name string
	plan probe.Plan
}

// probeTargets returns the rows that will be probed, in config order: those built from a
// plan. A row that could not be built is explained by its own build diagnostic, so no
// probing warning speaks about it.
func probeTargets(rows []entry) []probeTarget {
	targets := make([]probeTarget, 0, len(rows))
	for _, r := range rows {
		targets = append(targets, probeTarget{name: r.target.Name(), plan: r.plan})
	}

	return targets
}

// probeWarnings explains how the targets will be probed on this host: a missing ICMP
// privilege, the next-hop caveats, and the deprecated snmp method. (An attribute a method
// does not use is a build error, explained by its own diagnostic.)
func probeWarnings(targets []probeTarget, host HostCapabilities) []Diagnostic {
	warns := icmpPrivilegeWarnings(targets, host)
	warns = append(warns, nexthopWarnings(targets, host)...)

	return append(warns, snmpWarnings(targets)...)
}

// DirectICMPUnavailable is a config with direct probes on a host that can open neither a
// raw nor a datagram ICMP socket. Platform picks the remedy.
type DirectICMPUnavailable struct{ Platform probe.OS }

// NexthopICMPUnavailable is a config with forced next-hop probes on a host that cannot
// open the raw socket they need. Platform picks the remedy. (A host that cannot force a
// next hop at all never builds such a target: its build diagnostic says why.)
type NexthopICMPUnavailable struct{ Platform probe.OS }

func (DirectICMPUnavailable) diagnostic()    {}
func (DirectICMPUnavailable) probeWarning()  {}
func (NexthopICMPUnavailable) diagnostic()   {}
func (NexthopICMPUnavailable) probeWarning() {}

// icmpPrivilegeWarnings selects each target class whose socket cannot open here. The two
// classes are judged apart, since their needs differ: a host without the raw socket fails
// only the next-hop targets when the datagram socket opens, and both classes when it does
// not.
func icmpPrivilegeWarnings(targets []probeTarget, host HostCapabilities) []Diagnostic {
	need := localICMPNeeds(targets)

	var warns []Diagnostic
	if need.direct && !host.DirectICMPAvailable() {
		warns = append(warns, DirectICMPUnavailable{Platform: host.Platform()})
	}

	if need.nexthop && !host.RawICMPAvailable() {
		warns = append(warns, NexthopICMPUnavailable{Platform: host.Platform()})
	}

	return warns
}

// icmpNeeds records which local-ICMP target classes a config contains; each has a
// distinct socket-privilege requirement (see icmpPrivilegeWarnings).
type icmpNeeds struct {
	direct  bool // a default direct-ICMP target (raw OR datagram).
	nexthop bool // a forced next-hop target (AF_PACKET, needs CAP_NET_RAW).
}

// localICMPNeeds classifies the targets a config relies on, tracking the direct and
// next-hop classes separately because their privilege needs differ. The other methods
// need no ICMP socket here: tcp and quic are sent by hping3 and a UDP socket, and the
// relay methods (probe.RelayParams) by the relay.
func localICMPNeeds(targets []probeTarget) icmpNeeds {
	var n icmpNeeds

	for _, t := range targets {
		n.direct = n.direct || t.plan.Method() == probe.MethodDirect
		n.nexthop = n.nexthop || t.plan.Method() == probe.MethodNexthop
	}

	return n
}

// StrictRPFilter is a host whose strict IPv4 rp_filter may drop the replies to forced
// probes sent out another interface.
type StrictRPFilter struct{}

func (StrictRPFilter) diagnostic()   {}
func (StrictRPFilter) probeWarning() {}

// nexthopWarnings warns of strict rp_filter, which can drop the replies to
// cross-interface forced IPv4 probes, making a reachable host look down (there is no IPv6
// equivalent knob). Every forced target here was built, so this host can force it.
//
// A gateway that cannot force its target (not an address, or of the other family) never
// reaches here: its target could not be built, and its build diagnostic says why.
func nexthopWarnings(targets []probeTarget, host HostCapabilities) []Diagnostic {
	if forcesIPv4(targets) && host.RPFilterStrict() {
		return []Diagnostic{StrictRPFilter{}}
	}

	return nil
}

// forcesIPv4 reports whether some target is forced out an IPv4 gateway, judged by the
// same plans the forced-next-hop prober is built from, so a warning can never disagree
// with what the probe does.
func forcesIPv4(targets []probeTarget) bool {
	for _, t := range targets {
		if forced, ok := t.plan.Params().(probe.Nexthop); ok &&
			forced.Family() == probe.FamilyIPv4 {
			return true
		}
	}

	return false
}

// SNMPDeprecated names the targets probed by the snmp method, which is deprecated: few
// agents implement the remote ping of RFC 4560 it asks them for.
type SNMPDeprecated struct{ Targets []string }

func (SNMPDeprecated) diagnostic()   {}
func (SNMPDeprecated) probeWarning() {}

// snmpWarnings names the targets of the deprecated snmp method, if there are any.
func snmpWarnings(targets []probeTarget) []Diagnostic {
	var names []string

	for _, t := range targets {
		if t.plan.Method() == probe.MethodSNMP {
			names = append(names, t.name)
		}
	}

	if len(names) == 0 {
		return nil
	}

	return []Diagnostic{SNMPDeprecated{Targets: names}}
}
