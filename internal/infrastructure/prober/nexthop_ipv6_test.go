package prober

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestEchoIPv6Build(t *testing.T) {
	src := net.ParseIP("2001:db8::1")
	dst := net.ParseIP("2001:db8::2")
	token := []byte{0xde, 0xad, 0xbe, 0xef}

	pkt, err := echoIPv6{}.build(src, dst, 0x1234, 7, token)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if len(pkt) < ipv6HeaderLen+8 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}

	// Version nibble is 6 and the next header is ICMPv6.
	if pkt[0]>>4 != ipv6.Version {
		t.Errorf("version nibble = %d, want 6", pkt[0]>>4)
	}

	if pkt[6] != ianaProtocolICMPv6 {
		t.Errorf("next header = %d, want %d", pkt[6], ianaProtocolICMPv6)
	}

	// The payload-length field matches the ICMPv6 message length (no checksum field
	// in the IPv6 header itself).
	if plen := int(binary.BigEndian.Uint16(pkt[4:6])); plen != len(pkt)-ipv6HeaderLen {
		t.Errorf("payload length = %d, want %d", plen, len(pkt)-ipv6HeaderLen)
	}

	// Source/destination land in the header.
	if !net.IP(pkt[8:24]).Equal(src) || !net.IP(pkt[24:40]).Equal(dst) {
		t.Errorf(
			"header src/dst = %v/%v, want %v/%v",
			net.IP(pkt[8:24]),
			net.IP(pkt[24:40]),
			src,
			dst,
		)
	}

	icmpv6 := pkt[ipv6HeaderLen:]

	// The ICMPv6 checksum must be correct, not merely present: the Internet checksum
	// over the IPv6 pseudo-header followed by the ICMPv6 message (checksum field
	// included) folds to zero. AF_PACKET sends get no kernel-computed checksum, so
	// build() must produce it itself via icmp.Message.Marshal's pseudo-header path.
	if got := ipv6PseudoChecksum(pkt); got != 0 {
		t.Errorf("ICMPv6 checksum invalid: residual %#04x", got)
	}

	// The payload parses back to our echo request.
	msg, err := icmp.ParseMessage(ianaProtocolICMPv6, icmpv6)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		t.Fatalf("body type = %T, want *icmp.Echo", msg.Body)
	}

	if echo.ID != 0x1234 || echo.Seq != 7 {
		t.Errorf("echo id/seq = %d/%d, want 4660/7", echo.ID, echo.Seq)
	}

	if !bytes.Equal(echo.Data, token) {
		t.Errorf("echo data = %x, want %x", echo.Data, token)
	}
}

func TestSelectSrcV6(t *testing.T) {
	dualStack := []net.Addr{
		&net.IPNet{IP: net.ParseIP("fe80::abcd"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("2001:db8::5"), Mask: net.CIDRMask(64, 128)},
	}

	// A global target is answered from the global source.
	if src, ok := selectSrcV6(dualStack, net.ParseIP("2001:db8:1::1")); !ok ||
		!src.Equal(net.ParseIP("2001:db8::5")) {
		t.Errorf("global target: src=%v ok=%v, want 2001:db8::5", src, ok)
	}

	// A link-local target is answered from the link-local source.
	if src, ok := selectSrcV6(dualStack, net.ParseIP("fe80::1")); !ok ||
		!src.Equal(net.ParseIP("fe80::abcd")) {
		t.Errorf("link-local target: src=%v ok=%v, want fe80::abcd", src, ok)
	}

	// A link-local-only interface cannot answer a global target.
	llOnly := []net.Addr{
		&net.IPNet{IP: net.ParseIP("fe80::abcd"), Mask: net.CIDRMask(64, 128)},
	}
	if _, ok := selectSrcV6(llOnly, net.ParseIP("2001:db8::1")); ok {
		t.Error("global target from a link-local-only interface should fail")
	}

	// Matching ULA/GUA labels are preferred regardless of enumeration order.
	ulaThenGUA := []net.Addr{
		&net.IPNet{IP: net.ParseIP("fd00::5"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("2001:db8::5"), Mask: net.CIDRMask(64, 128)},
	}
	if src, ok := selectSrcV6(ulaThenGUA, net.ParseIP("2001:db8:1::1")); !ok ||
		!src.Equal(net.ParseIP("2001:db8::5")) {
		t.Errorf("GUA target (ULA enumerated first): src=%v ok=%v, want 2001:db8::5", src, ok)
	}

	if src, ok := selectSrcV6(ulaThenGUA, net.ParseIP("fd12:3456::1")); !ok ||
		!src.Equal(net.ParseIP("fd00::5")) {
		t.Errorf("ULA target: src=%v ok=%v, want fd00::5", src, ok)
	}

	// ULA and GUA both have global scope: fall back when the matching label is absent.
	ulaOnly := []net.Addr{
		&net.IPNet{IP: net.ParseIP("fd00::5"), Mask: net.CIDRMask(64, 128)},
	}

	src, ok := selectSrcV6(ulaOnly, net.ParseIP("2001:db8::1"))
	if !ok ||
		!src.Equal(net.ParseIP("fd00::5")) {
		t.Error("GUA target should fall back to the available ULA source")
	}

	guaOnly := []net.Addr{
		&net.IPNet{IP: net.ParseIP("2001:db8::5"), Mask: net.CIDRMask(64, 128)},
	}

	src, ok = selectSrcV6(guaOnly, net.ParseIP("fd12:3456::1"))
	if !ok ||
		!src.Equal(net.ParseIP("2001:db8::5")) {
		t.Error("ULA target should fall back to the available GUA source")
	}

	// IPv4 addresses are ignored.
	v4Only := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)},
	}
	if _, ok := selectSrcV6(v4Only, net.ParseIP("2001:db8::1")); ok {
		t.Error("IPv4-only interface should yield no IPv6 source")
	}
}

// An address destination is sent to as it is. One of the other family than the gateway,
// or with a zone (the gateway picks the egress), never gets here: Compile refuses it.
func TestResolveInFamily(t *testing.T) {
	ctx := t.Context()

	for addr, gateway := range map[string]string{
		"192.0.2.1":   "192.0.2.254",
		"2001:db8::1": "2001:db8::fe",
		"fe80::1":     "2001:db8::fe",
	} {
		gw := netip.MustParseAddr(gateway)
		plan := compiled(t, probe.Spec{Addr: addr, Params: probe.Nexthop{Gateway: gw}})

		n, ok := plan.Params().(probe.Nexthop)
		if !ok {
			t.Fatalf("%s: compiled %T", addr, plan.Params())
		}

		ip := resolveInFamily(ctx, plan.Destination(), n.Family())
		if want := net.IP(netip.MustParseAddr(addr).AsSlice()); !ip.Equal(want) {
			t.Errorf("%s: resolved %v, want %v", addr, ip, want)
		}
	}
}

// ipv6PseudoChecksum returns the Internet checksum of pkt's ICMPv6 message over the
// IPv6 pseudo-header — the header's source, destination and payload length, next header
// = ICMPv6 — followed by the message. A correct ICMPv6 checksum makes it fold to zero.
func ipv6PseudoChecksum(pkt []byte) uint16 {
	const pseudoLen = 40

	icmpv6 := pkt[ipv6HeaderLen:]

	buf := make([]byte, pseudoLen+len(icmpv6))
	copy(buf[0:32], pkt[8:40])
	binary.BigEndian.PutUint32(buf[32:36], uint32(binary.BigEndian.Uint16(pkt[4:6])))
	buf[39] = ianaProtocolICMPv6
	copy(buf[pseudoLen:], icmpv6)

	return ipChecksum(buf)
}

func TestFamilyFor(t *testing.T) {
	if _, ok := familyFor(net.ParseIP("192.0.2.1")).(echoIPv4); !ok {
		t.Error("familyFor(IPv4) is not echoIPv4")
	}

	if _, ok := familyFor(net.ParseIP("2001:db8::1")).(echoIPv6); !ok {
		t.Error("familyFor(IPv6) is not echoIPv6")
	}
}
