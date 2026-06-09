package ping

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"time"

	"golang.org/x/net/icmp"
	netipv4 "golang.org/x/net/ipv4" // aliased: the package "ipv4" name clashes with the local ipv4 IP-version constant in osname.go.
)

const (
	// etherTypeIPv4 is the EtherType for IPv4, carried in sll_protocol when sending.
	etherTypeIPv4 = 0x0800
	// ianaProtocolICMP is the IP protocol number for ICMPv4 (icmp.ParseMessage).
	ianaProtocolICMP = 1
	// ipv4TTL is the TTL stamped into forced echo requests.
	ipv4TTL = 64
	// recvBufSize bounds a single ICMP read.
	recvBufSize = 1500

	// ipv4ChecksumOff is the byte offset of the checksum field in an IPv4 header.
	ipv4ChecksumOff = 10
	// octetShift converts between a byte and the high half of a 16-bit word.
	octetShift = 8
	// word16Mask is the low 16 bits.
	word16Mask = 0xffff
	// carryShift folds the checksum's carry bits back in.
	carryShift = 16
)

// echoIPv4 implements echoFamily for IPv4: an ICMPv4 echo built at L3 (so the
// frame can be addressed to an arbitrary next-hop MAC), with replies read from a
// raw ICMP socket.
type echoIPv4 struct{}

func (echoIPv4) ethertype() uint16 { return etherTypeIPv4 }

// build returns an IPv4 header + ICMPv4 echo request, with both checksums filled.
// The IPv4 header checksum must be computed here: netipv4.Header.Marshal writes the
// Checksum field verbatim (it does not compute it), and AF_PACKET sends do not let
// the kernel fill it in. The ICMP checksum is computed by icmp.Message.Marshal.
func (echoIPv4) build(src, dst net.IP, id, seq int, token []byte) ([]byte, error) {
	msg := icmp.Message{
		Type: netipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: token},
	}

	payload, err := msg.Marshal(nil)
	if err != nil {
		return nil, err
	}

	h := netipv4.Header{
		Version:  netipv4.Version,
		Len:      netipv4.HeaderLen,
		TotalLen: netipv4.HeaderLen + len(payload),
		ID:       id & word16Mask,
		Flags:    0, // no Don't-Fragment: a small echo must never be wedged by PMTU.
		TTL:      ipv4TTL,
		Protocol: ianaProtocolICMP,
		Src:      src.To4(),
		Dst:      dst.To4(),
	}

	header, err := h.Marshal()
	if err != nil {
		return nil, err
	}

	// h.Checksum was 0, so the checksum field is 0; compute over the header and
	// write the result back into it.
	binary.BigEndian.PutUint16(header[ipv4ChecksumOff:], ipChecksum(header))

	return append(header, payload...), nil
}

func (echoIPv4) listen(src net.IP) (replyWaiter, error) {
	addr := "0.0.0.0"
	if src != nil {
		addr = src.String()
	}

	conn, err := icmp.ListenPacket("ip4:icmp", addr)
	if err != nil {
		return nil, err
	}

	pc := conn.IPv4PacketConn()
	// FlagTTL lets us report the reply TTL; ignore the error so a platform that
	// cannot set it still receives (TTL stays -1).
	_ = pc.SetControlMessage(netipv4.FlagTTL, true)

	return &ipv4Waiter{conn: conn, pc: pc}, nil
}

// ipv4Waiter reads ICMPv4 replies. A raw ICMP socket sees every host's ICMP
// (fan-out), so wait keeps reading until its own reply arrives or the deadline
// passes, filtering by source, id, and seq.
type ipv4Waiter struct {
	conn *icmp.PacketConn
	pc   *netipv4.PacketConn
}

func (w *ipv4Waiter) close() error { return w.conn.Close() }

func (w *ipv4Waiter) wait(
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

		if ttl, ok := matchEchoReply(buf[:n], cm, src, peer, id, seq, token); ok {
			return ttl, true, nil
		}
	}
}

// matchEchoReply reports whether a received datagram is the echo reply for our
// probe (id, seq, and the echoed token from peer), and its TTL. The token guards
// against accepting another process's reply: a raw ICMP socket also delivers ICMP
// addressed to other listeners, whose id/seq can collide with ours.
func matchEchoReply(
	b []byte,
	cm *netipv4.ControlMessage,
	src net.Addr,
	peer net.IP,
	id, seq int,
	token []byte,
) (int, bool) {
	if !addrIP(src).Equal(peer) {
		return -1, false
	}

	msg, err := icmp.ParseMessage(ianaProtocolICMP, b)
	if err != nil || msg.Type != netipv4.ICMPTypeEchoReply {
		return -1, false
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok || echo.ID != id || echo.Seq != seq || !bytes.Equal(echo.Data, token) {
		return -1, false
	}

	ttl := -1
	if cm != nil {
		ttl = cm.TTL
	}

	return ttl, true
}

func isTimeout(err error) bool {
	var nerr net.Error

	return errors.As(err, &nerr) && nerr.Timeout()
}

// addrIP extracts the IP from the address returned by a raw ICMP read.
func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	default:
		return nil
	}
}

// ipChecksum is the 16-bit one's-complement Internet checksum (RFC 1071).
func ipChecksum(b []byte) uint16 {
	var sum uint32

	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}

	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << octetShift
	}

	for sum > word16Mask {
		sum = (sum & word16Mask) + (sum >> carryShift)
	}

	return ^uint16(sum)
}
