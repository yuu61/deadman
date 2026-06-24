package ping

import (
	"encoding/binary"
	"net"
	"time"

	"golang.org/x/net/icmp"
	netipv6 "golang.org/x/net/ipv6" // aliased for symmetry with nexthop_ipv4.go's netipv4.
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
		Type: netipv6.ICMPTypeEchoRequest,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: token},
	}

	payload, err := msg.Marshal(icmp.IPv6PseudoHeader(src.To16(), dst.To16()))
	if err != nil {
		return nil, err
	}

	plen := uint16(len(payload)) //nolint:gosec // an echo payload fits a uint16.

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

func (echoIPv6) listen(src net.IP) (replyWaiter, error) {
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
		return nil, err
	}

	pc := conn.IPv6PacketConn()
	// FlagHopLimit lets us report the reply hop limit as the TTL; ignore the error so
	// a platform that cannot set it still receives (TTL stays -1).
	_ = pc.SetControlMessage(netipv6.FlagHopLimit, true)

	return &ipv6Waiter{conn: conn, pc: pc}, nil
}

// ipv6Waiter reads ICMPv6 replies. A raw ICMPv6 socket sees every host's ICMPv6
// (fan-out), so wait keeps reading until its own reply arrives or the deadline
// passes, filtering by source, id, seq, and token.
type ipv6Waiter struct {
	conn *icmp.PacketConn
	pc   *netipv6.PacketConn
}

func (w *ipv6Waiter) close() error { return w.conn.Close() }

// wait is a deliberate parallel mirror of ipv4Waiter.wait; keep the two in sync. They
// differ only in the per-family PacketConn type (whose ReadFrom yields a different
// control message) and the matcher called. The reply-match body itself is shared (matchEcho).
func (w *ipv6Waiter) wait(
	peer net.IP,
	id, seq int,
	token []byte,
	deadline time.Time,
) (int, bool, error) {
	buf := make([]byte, recvBufSize)

	for {
		err := w.conn.SetReadDeadline(deadline)
		if err != nil {
			return -1, false, err
		}

		n, cm, src, err := w.pc.ReadFrom(buf)
		if err != nil {
			if isTimeout(err) {
				return -1, false, nil
			}

			return -1, false, err
		}

		if hl, ok := matchEchoReplyV6(buf[:n], cm, src, peer, id, seq, token); ok {
			return hl, true, nil
		}
	}
}

// matchEchoReplyV6 reports whether a received datagram is the ICMPv6 echo reply for
// our probe (id, seq, and the echoed token from peer), and its hop limit. It extracts
// the hop limit from the IPv6 control message and defers the rest to matchEcho.
func matchEchoReplyV6(
	b []byte,
	cm *netipv6.ControlMessage,
	src net.Addr,
	peer net.IP,
	id, seq int,
	token []byte,
) (int, bool) {
	hl := -1
	if cm != nil {
		hl = cm.HopLimit
	}

	return matchEcho(b, src, peer, id, seq, token, echoKindV6, hl)
}
