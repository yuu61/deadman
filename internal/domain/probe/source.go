package probe

import (
	"fmt"
	"net/netip"
)

// Source is where a probe is sent from, as source= names it: a local address, or an
// interface by name. The zero value is an omitted source, which leaves the choice to the
// kernel (or to the relay's ping). Which of the two a written source is, is the config
// syntax's call, made once where it is read; every rule about what a method can send
// from is Compile's, and adapters only branch on the kind.
type Source struct {
	addr  netip.Addr
	iface string
}

// SourceAddr is a source address. A zoned IPv6 address keeps its zone.
func SourceAddr(a netip.Addr) Source { return Source{addr: a} }

// SourceInterface is a source interface named name.
func SourceInterface(name string) Source { return Source{iface: name} }

// IsSet reports whether a source was given.
func (s Source) IsSet() bool { return s.addr.IsValid() || s.iface != "" }

// Addr returns the source address, if the source is one.
func (s Source) Addr() (netip.Addr, bool) { return s.addr, s.addr.IsValid() }

// Interface returns the source interface's name, if the source is one.
func (s Source) Interface() (string, bool) { return s.iface, s.iface != "" }

// String spells the source: the address (with any zone) or the interface name, or ""
// when omitted.
func (s Source) String() string {
	if s.addr.IsValid() {
		return s.addr.String()
	}

	return s.iface
}

// normalize maps an IPv4-mapped source address to IPv4 as canonicalIP does for a
// destination (so a zoned mapped one, whose zone IPv4 would lose, is refused), and
// refuses an interface named like an address, which the config syntax reads as the
// address: the two kinds must never spell the same source, or an identity could not tell
// them apart.
func (s Source) normalize() (Source, error) {
	if s.addr.IsValid() {
		a, err := canonicalIP(s.addr.String(), s.addr)
		if err != nil {
			return Source{}, err
		}

		return SourceAddr(a), nil
	}

	if _, ok := ipLiteral(s.iface); ok {
		return Source{}, fmt.Errorf("source interface %q is an IP address", s.iface)
	}

	return s, nil
}

// probeFamily is the family a probe is sent in: the target literal's, else the explicit
// one, else a source address's, since a source address can only send in its own family.
// A source address of another family than the literal or the explicit one could never be
// honored — the kernel would send from elsewhere, or not at all — so it is an error.
func probeFamily(dest Destination, explicit Family, src Source) (Family, error) {
	f, err := resolveFamily(dest, explicit)
	if err != nil {
		return 0, err
	}

	a, ok := src.Addr()
	if !ok {
		return f, nil
	}

	if sf := familyOf(a); f == FamilyUnknown {
		f = sf
	} else if f != sf {
		return 0, fmt.Errorf("source %s is %s, but the probe is %s", a, sf, f)
	}

	return f, nil
}
