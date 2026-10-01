package probe

import (
	"fmt"
	"net/netip"
	"strconv"
)

// Family is the IP address family of a probe.
type Family int

// Address families. FamilyUnknown is a name, whose family is decided at resolution.
const (
	FamilyUnknown Family = iota
	FamilyIPv4
	FamilyIPv6
)

// String names the family for a reason text.
func (f Family) String() string {
	switch f {
	case FamilyIPv4:
		return "IPv4"
	case FamilyIPv6:
		return "IPv6"
	case FamilyUnknown:
		return "unknown"
	default:
		return "family(" + strconv.Itoa(int(f)) + ")"
	}
}

// resolveSpelling is the stable family identity, independent of configuration syntax.
func (f Family) resolveSpelling() string { return strconv.Itoa(int(f)) }

// resolveFamily is the family a probe of dest is sent in: an address's own, which an
// explicit family must agree with, or the explicit one (or none) for a name. Compile checks
// relay hostnames separately because those require an explicit family.
func resolveFamily(dest Destination, family Family) (Family, error) {
	if family != FamilyUnknown && family != FamilyIPv4 && family != FamilyIPv6 {
		return 0, fmt.Errorf("invalid address family %d", family)
	}

	literal := dest.Family()
	if literal != FamilyUnknown {
		if family != FamilyUnknown && family != literal {
			return 0, fmt.Errorf("address %q and family differ", dest)
		}

		return literal, nil
	}

	return family, nil
}

// canonicalIP is a, parsed from raw, in the one form netip spells it, an IPv4-mapped
// address as IPv4. An IPv4 address has no zone, so a zoned mapped one would lose it and is
// refused.
func canonicalIP(raw string, a netip.Addr) (netip.Addr, error) {
	if a.Is4In6() && a.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("IPv4-mapped address %q cannot have a zone", raw)
	}

	return a.Unmap(), nil
}

// ipLiteral parses addr as an IP literal (a zoned IPv6 one included); ok is false for a
// host name.
func ipLiteral(addr string) (netip.Addr, bool) {
	a, err := ParseIP(addr)

	return a, err == nil
}

// familyOf reports the family of a valid address, an IPv4-mapped one counting as IPv4.
func familyOf(a netip.Addr) Family {
	if a.Unmap().Is4() {
		return FamilyIPv4
	}

	return FamilyIPv6
}
