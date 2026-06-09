package ping

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"

	"golang.org/x/net/icmp"
	netipv6 "golang.org/x/net/ipv6"
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
	if pkt[0]>>4 != ipv6 {
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

	// The ICMPv6 checksum was computed (pseudo-header folded in by Marshal), so it is
	// not left zero.
	if icmpv6[2] == 0 && icmpv6[3] == 0 {
		t.Error("ICMPv6 checksum was not computed")
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

//nolint:dupl // the IPv4 and IPv6 reply-match tests are deliberate parallel mirrors.
func TestMatchEchoReplyV6(t *testing.T) {
	const (
		id   = 0xabcd
		seq  = 5
		hl   = 57
		peer = "2001:db8::9"
	)

	token := []byte{0x01, 0x02, 0x03, 0x04}

	reply, err := (&icmp.Message{
		Type: netipv6.ICMPTypeEchoReply,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: token},
	}).Marshal(nil)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}

	src := &net.IPAddr{IP: net.ParseIP(peer)}
	cm := &netipv6.ControlMessage{HopLimit: hl}

	// The matching reply is accepted, with its hop limit.
	gotHL, ok := matchEchoReplyV6(reply, cm, src, net.ParseIP(peer), id, seq, token)
	if !ok || gotHL != hl {
		t.Fatalf("matchEchoReplyV6 = %d, %v; want %d, true", gotHL, ok, hl)
	}

	// A reply from a different host, or with a different id/seq/token, is rejected.
	if _, ok := matchEchoReplyV6(
		reply,
		cm,
		&net.IPAddr{IP: net.ParseIP("2001:db8::a")},
		net.ParseIP(peer),
		id,
		seq,
		token,
	); ok {
		t.Error("matched a reply from the wrong source")
	}

	if _, ok := matchEchoReplyV6(reply, cm, src, net.ParseIP(peer), id+1, seq, token); ok {
		t.Error("matched a reply with the wrong id")
	}

	if _, ok := matchEchoReplyV6(reply, cm, src, net.ParseIP(peer), id, seq+1, token); ok {
		t.Error("matched a reply with the wrong seq")
	}

	if _, ok := matchEchoReplyV6(
		reply,
		cm,
		src,
		net.ParseIP(peer),
		id,
		seq,
		[]byte{0x09, 0x09, 0x09, 0x09},
	); ok {
		t.Error("matched a reply with the wrong token")
	}

	// An echo *request* (wrong ICMPv6 type) is not a reply.
	request, err := (&icmp.Message{
		Type: netipv6.ICMPTypeEchoRequest,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: token},
	}).Marshal(nil)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	if _, ok := matchEchoReplyV6(request, cm, src, net.ParseIP(peer), id, seq, token); ok {
		t.Error("matched an echo request as a reply")
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

	// IPv4 addresses are ignored.
	v4Only := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)},
	}
	if _, ok := selectSrcV6(v4Only, net.ParseIP("2001:db8::1")); ok {
		t.Error("IPv4-only interface should yield no IPv6 source")
	}
}

func TestResolveToFamily(t *testing.T) {
	ctx := context.Background()
	v4gw := net.ParseIP("192.0.2.254")
	v6gw := net.ParseIP("2001:db8::ffff")

	if ip := resolveToFamily(ctx, "192.0.2.1", v4gw); ip == nil || ip.To4() == nil {
		t.Errorf("v4 literal behind a v4 gateway = %v, want an IPv4 address", ip)
	}

	if ip := resolveToFamily(ctx, "192.0.2.1", v6gw); ip != nil {
		t.Errorf("v4 literal behind a v6 gateway = %v, want nil", ip)
	}

	if ip := resolveToFamily(ctx, "2001:db8::1", v6gw); ip == nil || ip.To4() != nil {
		t.Errorf("v6 literal behind a v6 gateway = %v, want an IPv6 address", ip)
	}

	if ip := resolveToFamily(ctx, "2001:db8::1", v4gw); ip != nil {
		t.Errorf("v6 literal behind a v4 gateway = %v, want nil", ip)
	}
}

func TestFamilyFor(t *testing.T) {
	if _, ok := familyFor(net.ParseIP("192.0.2.1")).(echoIPv4); !ok {
		t.Error("familyFor(IPv4) is not echoIPv4")
	}

	if _, ok := familyFor(net.ParseIP("2001:db8::1")).(echoIPv6); !ok {
		t.Error("familyFor(IPv6) is not echoIPv6")
	}
}
