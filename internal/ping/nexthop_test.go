package ping

import (
	"net"
	"testing"

	"golang.org/x/net/icmp"
	netipv4 "golang.org/x/net/ipv4"
)

func TestIPChecksum(t *testing.T) {
	// Canonical IPv4 header (checksum field zeroed) from the Wikipedia worked
	// example; the correct header checksum is 0xb1e6.
	header := []byte{
		0x45, 0x00, 0x00, 0x3c, 0x1c, 0x46, 0x40, 0x00,
		0x40, 0x06, 0x00, 0x00, 0xac, 0x10, 0x0a, 0x63,
		0xac, 0x10, 0x0a, 0x0c,
	}

	if got := ipChecksum(header); got != 0xb1e6 {
		t.Errorf("ipChecksum = %#04x, want 0xb1e6", got)
	}

	// With the checksum filled in, the checksum over the whole header is 0.
	header[10] = 0xb1
	header[11] = 0xe6

	if got := ipChecksum(header); got != 0 {
		t.Errorf("ipChecksum over a valid header = %#04x, want 0", got)
	}
}

func TestEchoIPv4Build(t *testing.T) {
	src := net.ParseIP("192.0.2.1")
	dst := net.ParseIP("198.51.100.2")

	pkt, err := echoIPv4{}.build(src, dst, 0x1234, 7)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if len(pkt) < 20+8 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}

	// The IPv4 header checksum must be valid (sum over the header is 0).
	if got := ipChecksum(pkt[:20]); got != 0 {
		t.Errorf("IPv4 header checksum invalid: residual %#04x", got)
	}

	// Source/destination land in the header.
	if !net.IP(pkt[12:16]).Equal(src.To4()) || !net.IP(pkt[16:20]).Equal(dst.To4()) {
		t.Errorf(
			"header src/dst = %v/%v, want %v/%v",
			net.IP(pkt[12:16]),
			net.IP(pkt[16:20]),
			src,
			dst,
		)
	}

	// Protocol is ICMP and DF is not set.
	if pkt[9] != ianaProtocolICMP {
		t.Errorf("protocol = %d, want %d", pkt[9], ianaProtocolICMP)
	}

	if pkt[6]&0x40 != 0 {
		t.Errorf("Don't-Fragment bit unexpectedly set: flags byte %#02x", pkt[6])
	}

	// The ICMP payload parses back to our echo request.
	msg, err := icmp.ParseMessage(ianaProtocolICMP, pkt[20:])
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
}

func TestPickOnLinkSrc(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.1.10"), Mask: net.CIDRMask(24, 32)},
	}

	src, ok := pickOnLinkSrc(addrs, net.ParseIP("192.168.1.1"))
	if !ok || !src.Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("pickOnLinkSrc = %v, %v; want 192.168.1.10, true", src, ok)
	}

	if _, ok := pickOnLinkSrc(addrs, net.ParseIP("172.16.0.1")); ok {
		t.Error("pickOnLinkSrc matched an off-link gateway")
	}
}

func TestAddrsHaveIP(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)},
	}

	if !addrsHaveIP(addrs, net.ParseIP("10.0.0.5")) {
		t.Error("addrsHaveIP should find 10.0.0.5")
	}

	if addrsHaveIP(addrs, net.ParseIP("10.0.0.6")) {
		t.Error("addrsHaveIP should not find 10.0.0.6")
	}
}

func TestSelectEgressErrors(t *testing.T) {
	// An IPv6 next-hop is rejected (this version forces IPv4 only).
	_, _, err := selectEgress(net.ParseIP("fe80::1"), "")
	if err == nil {
		t.Error("selectEgress with an IPv6 gateway should error")
	}

	// A non-existent interface name is rejected.
	_, _, err = selectEgress(net.ParseIP("192.0.2.1"), "deadman-no-such-iface0")
	if err == nil {
		t.Error("selectEgress with an unknown interface should error")
	}
}

func TestMatchEchoReply(t *testing.T) {
	const (
		id   = 0xabcd
		seq  = 5
		ttl  = 57
		peer = "203.0.113.9"
	)

	reply, err := (&icmp.Message{
		Type: netipv4.ICMPTypeEchoReply,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("deadman")},
	}).Marshal(nil)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}

	src := &net.IPAddr{IP: net.ParseIP(peer)}
	cm := &netipv4.ControlMessage{TTL: ttl}

	// The matching reply is accepted, with its TTL.
	gotTTL, ok := matchEchoReply(reply, cm, src, net.ParseIP(peer), id, seq)
	if !ok || gotTTL != ttl {
		t.Fatalf("matchEchoReply = %d, %v; want %d, true", gotTTL, ok, ttl)
	}

	// A reply from a different host, or with a different id/seq, is rejected.
	if _, ok := matchEchoReply(
		reply,
		cm,
		&net.IPAddr{IP: net.ParseIP("198.51.100.1")},
		net.ParseIP(peer),
		id,
		seq,
	); ok {
		t.Error("matched a reply from the wrong source")
	}

	if _, ok := matchEchoReply(reply, cm, src, net.ParseIP(peer), id+1, seq); ok {
		t.Error("matched a reply with the wrong id")
	}

	if _, ok := matchEchoReply(reply, cm, src, net.ParseIP(peer), id, seq+1); ok {
		t.Error("matched a reply with the wrong seq")
	}

	// An echo *request* (wrong ICMP type) is not a reply.
	request, err := (&icmp.Message{
		Type: netipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: id, Seq: seq},
	}).Marshal(nil)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	if _, ok := matchEchoReply(request, cm, src, net.ParseIP(peer), id, seq); ok {
		t.Error("matched an echo request as a reply")
	}
}

func TestNextProbeIDIs16Bit(t *testing.T) {
	for range 3 {
		if id := nextProbeID(); id < 0 || id > 0xffff {
			t.Fatalf("nextProbeID = %d, want a 16-bit value", id)
		}
	}
}
