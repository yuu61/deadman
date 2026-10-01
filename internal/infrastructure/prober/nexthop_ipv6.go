package prober

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"
)

const (
	// etherTypeIPv6 is the EtherType for IPv6, carried in sll_protocol when sending.
	etherTypeIPv6 = 0x86dd
	// ianaProtocolICMPv6 is the IP protocol / next-header number for ICMPv6.
	ianaProtocolICMPv6 = 58
	// ipv6HopLimit is the hop limit stamped into forced echo requests.
	ipv6HopLimit = 64
	// ipv6HeaderLen is the fixed IPv6 header size; unlike IPv4 it carries no checksum.
	ipv6HeaderLen = 40
	// ipv6VersionByte is the first header byte: version nibble 6, traffic class and
	// flow label left zero.
	ipv6VersionByte = 0x60
)

// echoIPv6 implements echoFamily for IPv6: an ICMPv6 echo built at L3 (so the frame
// can be addressed to an arbitrary next-hop MAC), with replies read from a raw
// ICMPv6 socket. The IPv6 header has no checksum of its own; the ICMPv6 checksum
// covers a pseudo-header (icmp.IPv6PseudoHeader), which Marshal folds in.
type echoIPv6 struct{}

func (echoIPv6) ethertype() uint16 { return etherTypeIPv6 }

// build returns a 40-byte IPv6 header followed by an ICMPv6 echo request. The ICMPv6
// checksum is computed by icmp.Message.Marshal once it is given the pseudo-header;
// the IPv6 header itself needs no checksum.
func (echoIPv6) build(src, dst net.IP, id, seq int, token []byte) ([]byte, error) {
	msg := icmp.Message{
		Type: ipv6.ICMPTypeEchoRequest,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: token},
	}

	payload, err := msg.Marshal(icmp.IPv6PseudoHeader(src.To16(), dst.To16()))
	if err != nil {
		return nil, fmt.Errorf("marshal the ICMPv6 echo: %w", err)
	}

	// The Payload Length field is 16 bits; a jumbogram needs an extension header.
	n := len(payload)
	if n > math.MaxUint16 {
		return nil, fmt.Errorf("nexthop: ICMPv6 message of %d bytes exceeds an IPv6 payload", n)
	}

	plen := uint16(n)

	// Lay the 40-byte header and the ICMPv6 payload into one buffer by index (not
	// append): bytes 1-3 (traffic class / flow label) stay zero.
	pkt := make([]byte, ipv6HeaderLen+len(payload))
	pkt[0] = ipv6VersionByte
	binary.BigEndian.PutUint16(pkt[4:6], plen)
	pkt[6] = ianaProtocolICMPv6 // next header.
	pkt[7] = ipv6HopLimit
	copy(pkt[8:24], src.To16())
	copy(pkt[24:40], dst.To16())
	copy(pkt[ipv6HeaderLen:], payload)

	return pkt, nil
}

func (echoIPv6) listen(src net.IP, id int) (replyWaiter, error) {
	// Bind the source when it is routable (global or ULA): the reply returns to it,
	// and binding makes a vanished source (e.g. a SLAAC/DHCPv6 address rotated out
	// from under a long-running monitor) fail here, so the pinger re-selects next
	// round — the same self-heal the IPv4 path gets from binding its source. A
	// link-local source falls back to the wildcard: its bind would need a zone, and
	// link-local addresses do not rotate. The userspace match (peer + id + seq +
	// token) isolates our reply from the raw socket's fan-out either way.
	addr := "::"
	if src != nil && !src.IsLinkLocalUnicast() {
		addr = src.String()
	}

	conn, err := icmp.ListenPacket("ip6:ipv6-icmp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen for ICMPv6 replies: %w", err)
	}

	err = filterEcho(conn, id)
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	return &echoWaiter{conn: conn, kind: echoKindV6}, nil
}
