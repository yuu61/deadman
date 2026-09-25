package ping

import (
	"bytes"
	"net"

	"golang.org/x/net/icmp"
	netipv4 "golang.org/x/net/ipv4" // aliased: see nexthop_ipv4.go for why.
	netipv6 "golang.org/x/net/ipv6" // aliased for symmetry with netipv4.
)

// echoReplyKind selects the address family for matchEcho: the IP protocol number
// passed to icmp.ParseMessage and the ICMP type a reply must carry. Bundling the two
// keeps matchEcho within the argument limit and names the IPv4/IPv6 difference.
type echoReplyKind struct {
	proto     int
	replyType icmp.Type
}

var (
	echoKindV4 = echoReplyKind{ianaProtocolICMP, netipv4.ICMPTypeEchoReply}
	echoKindV6 = echoReplyKind{ianaProtocolICMPv6, netipv6.ICMPTypeEchoReply}
)

// matchEcho reports whether b is this probe's echo reply — from peer, of kind's family,
// carrying our id, seq and echoed token — returning the caller-supplied ttl on a match.
// The token guards against accepting another listener's reply on the fan-out raw socket.
// It is the shared body behind matchEchoReply (IPv4) and matchEchoReplyV6 (IPv6); the
// families differ only in kind and in where ttl is read (cm.TTL vs cm.HopLimit).
func matchEcho(
	b []byte,
	src net.Addr,
	peer net.IP,
	id, seq int,
	token []byte,
	kind echoReplyKind,
	ttl int,
) (int, bool) {
	if !addrIP(src).Equal(peer) {
		return -1, false
	}

	msg, err := icmp.ParseMessage(kind.proto, b)
	if err != nil || msg.Type != kind.replyType {
		return -1, false
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok || echo.ID != id || echo.Seq != seq || !bytes.Equal(echo.Data, token) {
		return -1, false
	}

	return ttl, true
}
