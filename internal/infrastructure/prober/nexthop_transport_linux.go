//go:build linux

package prober

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// AF_PACKET-based link transport. Sending a frame whose L2 destination is the
// gateway's MAC is what forces the next-hop; the kernel adds the Ethernet header
// for SOCK_DGRAM, so we hand it the L3 packet and the destination MAC.

const (
	// neighborResolveTimeout bounds how long resolveGateway waits for the kernel to
	// populate the neighbor cache (ARP or NDP) after a nudge.
	neighborResolveTimeout = 500 * time.Millisecond
	// neighborPollInterval is the neighbor-cache re-read cadence.
	neighborPollInterval = 50 * time.Millisecond
	// neighborKickPort is the (discard) UDP port we write to so the kernel resolves
	// the gateway; the datagram itself is irrelevant.
	neighborKickPort = "9"
)

// htons converts a uint16 from host to network byte order: the big-endian bytes of v,
// read back in the host's byte order (the identity on a big-endian host).
func htons(v uint16) uint16 {
	var b [2]byte

	binary.BigEndian.PutUint16(b[:], v)

	return binary.NativeEndian.Uint16(b[:])
}

type afpacketTransport struct{}

// newLinkTransport returns the Linux AF_PACKET transport. It is stateless: each
// send opens and closes its own socket, so concurrent probes never share an fd.
func newLinkTransport() (linkTransport, error) { return afpacketTransport{}, nil }

func (afpacketTransport) send(
	iface *net.Interface,
	dstMAC net.HardwareAddr,
	ethertype uint16,
	l3 []byte,
) error {
	proto := int(htons(ethertype))

	fd, err := unix.Socket(
		unix.AF_PACKET,
		unix.SOCK_DGRAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC,
		proto,
	)
	if err != nil {
		return fmt.Errorf("open a packet socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	// sockaddr_ll carries at most 8 address bytes; a longer one would be sent truncated.
	var addr [8]byte

	n := len(dstMAC)
	if n > len(addr) {
		return fmt.Errorf(
			"nexthop: link-layer address %s is longer than %d bytes",
			dstMAC,
			len(addr),
		)
	}

	copy(addr[:], dstMAC)

	halen := uint8(n)

	// The destination MAC and egress ifindex go on the Sendto sockaddr, not a
	// bind: with SOCK_DGRAM the kernel frames the L3 packet to this MAC out this
	// interface.
	ll := unix.SockaddrLinklayer{
		Protocol: htons(ethertype),
		Ifindex:  iface.Index,
		Halen:    halen,
		Addr:     addr,
	}

	err = unix.Sendto(fd, l3, 0, &ll)
	if err != nil {
		return fmt.Errorf("send a link-layer frame: %w", err)
	}

	return nil
}

// resolveGateway reads the gateway's MAC from the kernel neighbor cache (ARP for IPv4,
// NDP for IPv6), through a targeted netlink RTM_GETNEIGH query, which also tells the entry's NUD
// state. It prefers a REACHABLE entry: a merely STALE one can hold an outdated MAC (a
// gateway replaced, or an address moved, with no gratuitous ARP or NA), and because an
// AF_PACKET send bypasses the kernel neighbor subsystem nothing else would ever
// revalidate it. When the entry is not REACHABLE it kicks the kernel (a real datagram to
// the gateway) to drive NUD, letting the kernel's own ARP / NDP state machine own
// resolution rather than racing it, then polls for the refreshed entry, falling back to
// the stale MAC if revalidation does not complete in time. Recovery from a changed MAC is
// eventual, not immediate: the entry must traverse DELAY/PROBE (and FAILED, when the old
// MAC is gone), which spans a few failed rounds — during which the stale MAC is reused
// and the host correctly reads X — before the new MAC is learned.
func (afpacketTransport) resolveGateway(
	ctx context.Context,
	iface *net.Interface,
	nexthop net.IP,
) (net.HardwareAddr, error) {
	mac, err := resolveNeighbor(ctx,
		func(ctx context.Context) (net.HardwareAddr, neighState, error) {
			return neighLookup(ctx, iface.Index, nexthop)
		},
		func(ctx context.Context) { kickNeighbor(ctx, iface, nexthop) },
	)
	if err != nil {
		return nil, fmt.Errorf("nexthop: %s on %s: %w", nexthop, iface.Name, err)
	}

	return mac, nil
}

// resolveNeighbor waits for revalidation within both the parent's lifetime and
// the neighbor budget. Lookup errors observed no gateway and must not count as loss.
func resolveNeighbor(
	parent context.Context,
	lookup func(context.Context) (net.HardwareAddr, neighState, error),
	kick func(context.Context),
) (net.HardwareAddr, error) {
	ctx, cancel := context.WithTimeout(parent, neighborResolveTimeout)
	defer cancel()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	mac, state, err := lookup(ctx)
	if err != nil || state == neighReachable {
		return mac, err
	}

	kick(ctx)

	ticker := time.NewTicker(neighborPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return neighborFallback(parent, mac, state)
		case <-ticker.C:
			nextMAC, nextState, lookupErr := lookup(ctx)
			if ctx.Err() != nil {
				continue
			}

			if lookupErr != nil || nextState == neighReachable {
				return nextMAC, lookupErr
			}

			mac, state = nextMAC, nextState
		}
	}
}

// Keep the latest usable MAC while the kernel's NUD revalidation continues,
// unless the parent canceled the entire probe.
func neighborFallback(
	ctx context.Context,
	mac net.HardwareAddr,
	state neighState,
) (net.HardwareAddr, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if state != neighMissing {
		return mac, nil
	}

	return nil, errGatewayUnresolved
}

// kickNeighbor nudges the kernel to resolve gw by writing a datagram toward it, so
// the neighbor entry (ARP for IPv4, NDP for IPv6) lands in the cache. The socket is
// bound to iface (SO_BINDTODEVICE) so the entry lands on the intended egress device —
// otherwise normal/policy routing could resolve gw on a different interface and the
// later cache lookup by interface would miss. A link-local gateway also needs the
// zone on the address, or the dial cannot pick the link. Errors are ignored: the
// only purpose is the side effect of triggering resolution.
func kickNeighbor(ctx context.Context, iface *net.Interface, gw net.IP) {
	ctx, cancel := context.WithTimeout(ctx, neighborPollInterval)
	defer cancel()

	host := gw.String()
	if gw.IsLinkLocalUnicast() {
		host += "%" + iface.Name
	}

	dialer := net.Dialer{
		Timeout: neighborPollInterval,
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error

			cerr := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptString(
					int(fd),
					unix.SOL_SOCKET,
					unix.SO_BINDTODEVICE,
					iface.Name,
				)
			})
			if cerr != nil {
				return cerr
			}

			if serr != nil {
				return fmt.Errorf("bind the socket to %s: %w", iface.Name, serr)
			}

			return nil
		},
	}

	c, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(host, neighborKickPort))
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	deadline, _ := ctx.Deadline()

	err = c.SetWriteDeadline(deadline)
	if err != nil {
		return
	}

	stop := interruptOnDone(ctx, c.SetWriteDeadline)
	defer stop()

	// Only the side effect matters: the write makes the kernel resolve the neighbor.
	_, err = c.Write([]byte{0})
	discard(err)
}

// neighState classifies the gateway's neighbor cache entry.
type neighState int

const (
	neighMissing   neighState = iota // no usable entry.
	neighStale                       // usable but unconfirmed (STALE/DELAY/PROBE); MAC may be outdated.
	neighReachable                   // REACHABLE or PERMANENT.
)

// The NUD states whose entry has a link-layer address: the confirmed ones, and every
// usable one, which adds those awaiting confirmation.
const (
	nudReachable uint16 = unix.NUD_REACHABLE | unix.NUD_PERMANENT
	nudUsable    uint16 = nudReachable | unix.NUD_STALE | unix.NUD_DELAY | unix.NUD_PROBE
)

// neighEntry reads one message of the dump as the gateway's entry. It counts only when
// it is of family, on ifindex, matches ip, carries a link-layer address, and is in a
// state that has resolved one (REACHABLE/STALE/DELAY/PROBE/PERMANENT — not
// INCOMPLETE/FAILED); any other message is neighMissing.
func neighEntry(
	m *syscall.NetlinkMessage,
	family, ifindex int,
	ip net.IP,
) (net.HardwareAddr, neighState) {
	nd, ok := neighbor(m)
	if !ok || int(nd.Family) != family || int(nd.Ifindex) != ifindex ||
		nd.State&nudUsable == 0 {
		return nil, neighMissing
	}

	mac, ok := neighMAC(m.Data[unix.SizeofNdMsg:], ip)
	switch {
	case !ok:
		return nil, neighMissing
	case nd.State&nudReachable != 0:
		return mac, neighReachable
	default:
		return mac, neighStale
	}
}

// neighbor decodes the struct ndmsg that heads a neighbor message; ok is false for
// another message type or a message too short to hold one.
func neighbor(m *syscall.NetlinkMessage) (unix.NdMsg, bool) {
	var nd unix.NdMsg

	if m.Header.Type != uint16(unix.RTM_NEWNEIGH) {
		return nd, false
	}

	_, err := binary.Decode(m.Data, binary.NativeEndian, &nd)

	return nd, err == nil
}

// neighMAC walks the rtattrs of one neighbor message and returns its link-layer
// address when NDA_DST matches ip. syscall.ParseNetlinkRouteAttr does not handle
// neighbor messages, so the attributes are walked by hand: each is a uint16 length,
// a uint16 type, the payload, then padding to a 4-byte boundary.
func neighMAC(attrs []byte, ip net.IP) (net.HardwareAddr, bool) {
	const rtaHdrLen = 4

	var (
		dst net.IP
		mac net.HardwareAddr
	)

	for len(attrs) >= rtaHdrLen {
		alen := int(binary.NativeEndian.Uint16(attrs[0:2]))
		atype := binary.NativeEndian.Uint16(attrs[2:4])

		if alen < rtaHdrLen || alen > len(attrs) {
			break
		}

		switch atype {
		case uint16(unix.NDA_DST):
			dst = net.IP(append([]byte(nil), attrs[rtaHdrLen:alen]...))
		case uint16(unix.NDA_LLADDR):
			mac = net.HardwareAddr(append([]byte(nil), attrs[rtaHdrLen:alen]...))
		default:
			// other neighbor attributes (probes, cache info, …) are not needed.
		}

		adv := rtaAlign(alen)
		if adv > len(attrs) {
			break
		}

		attrs = attrs[adv:]
	}

	if dst.Equal(ip) && len(mac) > 0 {
		return mac, true
	}

	return nil, false
}

// rtaAlign rounds n up to the netlink rtattr alignment boundary (4 bytes).
func rtaAlign(n int) int {
	const align = 4

	return (n + align - 1) &^ (align - 1)
}
