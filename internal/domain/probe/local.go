package probe

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// The parameters of the methods that send the probe from this host: Direct and Nexthop
// (ICMP echo), TCP (hping3 SYN) and QUIC (a handshake). Each type sits with how Compile
// fills it in and which of its fields make up the Identity.

// LocalParams is the closed set of the parameters of the methods whose probe this host
// sends itself, as opposed to RelayParams. A switch over Params may take either set in
// one case and cover its members in an inner switch.
//
//sumtype:decl
type LocalParams interface {
	Params
	local()
}

// defaultQUICALPN is the protocol offered by a QUIC probe without an explicit ALPN.
const defaultQUICALPN = "h3"

// defaultQUICPort is the port a QUIC probe dials without an explicit one.
const defaultQUICPort = 443

const maxALPNLength = 255

// Direct is a direct ICMP echo from this host (MethodDirect).
type Direct struct {
	Source Source // source=: the source address or interface; omitted lets the kernel choose.
	// Family is resolve_family=: the family a name resolves in. Omitted (FamilyUnknown), it
	// is a literal's family, or a source address's, or else either.
	Family Family
}

func (Direct) local() {}

func (Direct) method() Method { return MethodDirect }

func (d Direct) compile(dest Destination) (Params, error) {
	src, err := d.Source.normalize()
	if err != nil {
		return nil, err
	}

	f, err := probeFamily(dest, d.Family, src)
	if err != nil {
		return nil, err
	}

	d.Source, d.Family = src, f

	return d, nil
}

func (d Direct) identity(b *strings.Builder) {
	writeKey(b, d.Source.String())
	writeKey(b, d.Family.resolveSpelling())
}

// TCP is a TCP SYN probe by hping3 (MethodTCP). hping3 is IPv4 only: it reads its target
// with inet_addr and gethostbyname, so a host name resolves to its IPv4 address there and
// an IPv6 literal fails every probe ("Unable to resolve").
type TCP struct {
	Port Port // port=: the destination port; required.
}

func (TCP) local() {}

func (TCP) method() Method { return MethodTCP }

func (t TCP) compile(dest Destination) (Params, error) {
	port, err := t.Port.resolve(MethodTCP, Port{})
	if err != nil {
		return nil, err
	}

	if dest.Family() == FamilyIPv6 {
		return nil, fmt.Errorf("tcp probes by hping3, which is IPv4 only, but %s is IPv6", dest)
	}

	t.Port = port

	return t, nil
}

func (t TCP) identity(b *strings.Builder) { writeKey(b, t.Port.String()) }

// QUIC times a QUIC handshake with the target (MethodQUIC).
type QUIC struct {
	Port   Port   // port=, 443 by default.
	ALPN   string // alpn=, "h3" by default.
	SNI    string // sni=; by default the target's host name, or none for an IP literal.
	Family Family // resolve_family=, as for Direct.
	// Verify is verify=, off by default: QUIC probes commonly target an endpoint by IP,
	// where a certificate check would fail every probe.
	Verify Verification
}

func (QUIC) local() {}

func (QUIC) method() Method { return MethodQUIC }

func (q QUIC) compile(dest Destination) (Params, error) {
	port, err := q.Port.resolve(MethodQUIC, PortNumber(defaultQUICPort))
	if err != nil {
		return nil, err
	}

	q.Port = port

	v, err := q.Verify.resolve(VerifyDisabled)
	if err != nil {
		return nil, err
	}

	q.Verify = v

	family, err := resolveFamily(dest, q.Family)
	if err != nil {
		return nil, err
	}

	q.Family = family
	if q.ALPN == "" {
		q.ALPN = defaultQUICALPN
	}

	if len(q.ALPN) > maxALPNLength {
		return nil, errors.New("ALPN exceeds 255 bytes")
	}

	if name, ok := dest.Name(); ok && q.SNI == "" {
		q.SNI = name
	}

	return q, nil
}

func (q QUIC) identity(b *strings.Builder) {
	writeKey(b, q.Port.String())
	writeKey(b, q.Family.resolveSpelling())
	writeKey(b, q.ALPN)
	writeKey(b, q.SNI)
	writeKey(b, q.Verify.resolveSpelling())
}

// Nexthop forces a direct ICMP echo out through a gateway (MethodNexthop). Forcing is
// same-family only (IPv4 uses ARP, IPv6 NDP), so Compile accepts only a gateway of the
// target literal's family; a name target is resolved in the gateway's family.
type Nexthop struct {
	Gateway netip.Addr // nexthop=: an IP address, unmapped and without a zone.
	// Source is source=: an interface fixes the egress, an address the source (of the
	// gateway's family, without a zone). An IPv6 link-local gateway is on-link on every
	// interface, so it needs the interface.
	Source Source
}

// Family returns the family the probe is forced in: the gateway's.
func (n Nexthop) Family() Family { return familyOf(n.Gateway) }

func (Nexthop) local() {}

func (Nexthop) method() Method { return MethodNexthop }

func (n Nexthop) compile(dest Destination) (Params, error) {
	if !n.Gateway.IsValid() {
		return nil, errors.New("nexthop requires an IP gateway")
	}

	if n.Gateway.Zone() != "" {
		return nil, errors.New("gateway must not have a zone; use source for its interface")
	}

	n.Gateway = n.Gateway.Unmap()
	if target := dest.Family(); target != FamilyUnknown && target != n.Family() {
		return nil, errors.New("gateway and target differ in IP family")
	}

	// The probe leaves by the gateway's link, so a target's zone would name an interface
	// the probe never uses.
	if a, ok := dest.IP(); ok && a.Zone() != "" {
		return nil, errors.New(
			"target must not have a zone; use source for the gateway's interface",
		)
	}

	src, err := nexthopSource(n)
	if err != nil {
		return nil, err
	}

	n.Source = src

	return n, nil
}

// nexthopSource checks the source a forced probe can use: an IPv6 link-local gateway
// needs the interface, and a source address must be of the gateway's family and name its
// interface through the egress, not a zone.
func nexthopSource(nexthop Nexthop) (Source, error) {
	src, err := nexthop.Source.normalize()
	if err != nil {
		return Source{}, err
	}

	_, iface := src.Interface()
	if nexthop.Gateway.Is6() && nexthop.Gateway.IsLinkLocalUnicast() && !iface {
		return Source{}, fmt.Errorf(
			"link-local gateway %s requires source=IFNAME for its interface",
			nexthop.Gateway,
		)
	}

	a, ok := src.Addr()
	if !ok {
		return src, nil
	}

	if a.Zone() != "" {
		return Source{}, errors.New(
			"source address must not have a zone; use source=IFNAME for the interface",
		)
	}

	if familyOf(a) != nexthop.Family() {
		return Source{}, fmt.Errorf(
			"source %s is %s, but the gateway is %s",
			a,
			familyOf(a),
			nexthop.Family(),
		)
	}

	return src, nil
}

func (n Nexthop) identity(b *strings.Builder) {
	writeKey(b, n.Gateway.String())
	writeKey(b, n.Source.String())
}
