package probe

import "net/netip"

// Destination is the target a plan probes, as Compile fixes it: an IP address in its
// canonical spelling (an IPv4-mapped one as IPv4, a zoned IPv6 one with its zone), or a
// host name as written, which each method resolves where it sends from. Only Compile
// makes one, so an adapter branches on its kind and never reads a written address again.
type Destination struct {
	ip   netip.Addr
	name string
}

// parseDestination reads a written target address: an IP literal (a zoned IPv6 one
// included) is an address, anything else a name. Two spellings of an address are one
// destination, and an IPv4-mapped one is IPv4, the family every method probes it in (a
// relay's `ping -4` and hping3 cannot parse ::ffff:192.0.2.1). A name no probe could
// resolve is refused (see checkName).
func parseDestination(addr string) (Destination, error) {
	a, ok := ipLiteral(addr)
	if !ok {
		err := checkName("address", addr)
		if err != nil {
			return Destination{}, err
		}

		return Destination{name: addr}, nil
	}

	ip, err := canonicalIP(addr, a)
	if err != nil {
		return Destination{}, err
	}

	return Destination{ip: ip}, nil
}

// IP returns the destination's address, if it is one.
func (d Destination) IP() (netip.Addr, bool) { return d.ip, d.ip.IsValid() }

// Name returns the destination's host name, if it is one.
func (d Destination) Name() (string, bool) { return d.name, !d.ip.IsValid() }

// Family is the family of an address, or FamilyUnknown for a name, whose family is
// decided where it resolves.
func (d Destination) Family() Family {
	if !d.ip.IsValid() {
		return FamilyUnknown
	}

	return familyOf(d.ip)
}

// String spells the destination: the address in its canonical form (with any zone), or
// the name as written.
func (d Destination) String() string {
	if d.ip.IsValid() {
		return d.ip.String()
	}

	return d.name
}
