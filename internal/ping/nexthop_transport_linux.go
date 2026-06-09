//go:build linux

package ping

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
	// atfCom is the /proc/net/arp flag bit for a complete (resolved) entry.
	atfCom = 0x2
	// neighborKickPort is the (discard) UDP port we write to so the kernel resolves
	// the gateway; the datagram itself is irrelevant.
	neighborKickPort = "9"
)

// nativeBigEndian is true on big-endian hosts, where network order equals host
// order and htons is the identity.
var nativeBigEndian = binary.NativeEndian.Uint16([]byte{0, 1}) == 1

// htons converts a uint16 from host to network byte order.
func htons(v uint16) uint16 {
	if nativeBigEndian {
		return v
	}

	return v<<8 | v>>8
}

type afpacketTransport struct{}

// newLinkTransport returns the Linux AF_PACKET transport. It is stateless: each
// send opens and closes its own socket, so concurrent probes never share an fd.
func newLinkTransport() linkTransport { return afpacketTransport{} }

func (afpacketTransport) send(
	iface *net.Interface,
	dstMAC net.HardwareAddr,
	ethertype uint16,
	l3 []byte,
) error {
	proto := int(htons(ethertype))

	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM, proto)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()

	var addr [8]byte
	copy(addr[:], dstMAC)

	halen := uint8(len(dstMAC)) //nolint:gosec // MAC length is at most 6, fits a byte

	// The destination MAC and egress ifindex go on the Sendto sockaddr, not a
	// bind: with SOCK_DGRAM the kernel frames the L3 packet to this MAC out this
	// interface.
	ll := unix.SockaddrLinklayer{
		Protocol: htons(ethertype),
		Ifindex:  iface.Index,
		Halen:    halen,
		Addr:     addr,
	}

	return unix.Sendto(fd, l3, 0, &ll)
}

// resolveGateway reads the gateway's MAC from the kernel neighbor cache, nudging the
// kernel to resolve it first if the entry is missing. Letting the kernel's own ARP /
// NDP state machine own resolution avoids racing it. The cache source differs by
// family: /proc/net/arp for IPv4, a netlink RTM_GETNEIGH dump for IPv6 (which has no
// such proc file).
func (afpacketTransport) resolveGateway(
	iface *net.Interface,
	nexthop net.IP,
) (net.HardwareAddr, error) {
	if nexthop.To4() != nil {
		return resolveGatewayARP(iface, nexthop)
	}

	return resolveGatewayNDP(iface, nexthop)
}

// resolveGatewayARP resolves an IPv4 gateway MAC via /proc/net/arp, nudging the
// kernel to ARP it if the entry is missing. /proc/net/arp does not expose the NUD
// state, so a complete entry is taken at face value (matching the original behavior).
func resolveGatewayARP(iface *net.Interface, nexthop net.IP) (net.HardwareAddr, error) {
	name, target := iface.Name, nexthop.String()

	if mac, ok := arpLookup(name, target); ok {
		return mac, nil
	}

	// connect() alone does not trigger ARP; an actual write to the on-link gateway
	// makes the kernel resolve it. Then re-read the cache until it lands.
	kickNeighbor(iface, nexthop)

	deadline := time.Now().Add(neighborResolveTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(neighborPollInterval)

		if mac, ok := arpLookup(name, target); ok {
			return mac, nil
		}
	}

	return nil, fmt.Errorf("nexthop: cannot resolve MAC for %s on %s", nexthop, iface.Name)
}

// resolveGatewayNDP resolves an IPv6 gateway MAC via the NDP cache. It prefers a
// REACHABLE entry: a merely STALE one can hold an outdated MAC (a gateway MAC change
// with no gratuitous NA, e.g. an HA failover), and because an AF_PACKET send bypasses
// the kernel neighbor subsystem nothing else would ever revalidate it. When the entry
// is not REACHABLE it kicks the kernel (a real datagram to the gateway) to drive NUD,
// then polls for the refreshed entry, falling back to the stale MAC if revalidation
// does not complete in time. Recovery from a changed MAC is eventual, not immediate:
// the entry must first age to STALE and then traverse DELAY/PROBE, which spans several
// failed rounds (tens of seconds) — during which the stale MAC is reused and the host
// correctly reads X — before the new MAC is learned.
func resolveGatewayNDP(iface *net.Interface, nexthop net.IP) (net.HardwareAddr, error) {
	if mac, st := ndpLookup(iface.Index, nexthop); st == ndpReachable {
		return mac, nil
	}

	kickNeighbor(iface, nexthop)

	deadline := time.Now().Add(neighborResolveTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(neighborPollInterval)

		if mac, st := ndpLookup(iface.Index, nexthop); st == ndpReachable {
			return mac, nil
		}
	}

	// Revalidation did not reach REACHABLE in time: reuse any usable (stale) entry —
	// its MAC is usually still correct — rather than failing a maybe-reachable host.
	if mac, st := ndpLookup(iface.Index, nexthop); st != ndpMissing {
		return mac, nil
	}

	return nil, fmt.Errorf("nexthop: cannot resolve MAC for %s on %s", nexthop, iface.Name)
}

// arpLookup returns the complete neighbor-cache entry for ip on ifname, if any.
// /proc/net/arp columns: IP, HW type, Flags, HW address, Mask, Device.
func arpLookup(ifname, ip string) (net.HardwareAddr, bool) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Scan() // header row.

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 || fields[0] != ip || fields[5] != ifname {
			continue
		}

		flags, err := strconv.ParseInt(fields[2], 0, 0)
		if err != nil || flags&atfCom == 0 {
			continue // incomplete entry.
		}

		mac, err := net.ParseMAC(fields[3])
		if err != nil {
			continue
		}

		return mac, true
	}

	if sc.Err() != nil {
		return nil, false
	}

	return nil, false
}

// kickNeighbor nudges the kernel to resolve gw by writing a datagram toward it, so
// the neighbor entry (ARP for IPv4, NDP for IPv6) lands in the cache. The socket is
// bound to iface (SO_BINDTODEVICE) so the entry lands on the intended egress device —
// otherwise normal/policy routing could resolve gw on a different interface and the
// later cache lookup by interface would miss. A link-local gateway also needs the
// zone on the address, or the dial cannot pick the link. Errors are ignored: the
// only purpose is the side effect of triggering resolution.
func kickNeighbor(iface *net.Interface, gw net.IP) {
	host := gw.String()
	if gw.IsLinkLocalUnicast() {
		host += "%" + iface.Name
	}

	d := net.Dialer{
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

			return serr
		},
	}

	c, err := d.DialContext(context.Background(), "udp", net.JoinHostPort(host, neighborKickPort))
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	_, _ = c.Write([]byte{0})
}

// ndpLookup reads the gateway's MAC from the IPv6 neighbor (NDP) cache. Unlike IPv4
// there is no /proc file for it, so we ask the kernel with a netlink RTM_GETNEIGH
// dump and parse the RTM_NEWNEIGH replies. An entry counts only when it is on
// ifindex, matches ip, carries a link-layer address, and is in a state that has
// resolved one (REACHABLE/STALE/DELAY/PROBE/PERMANENT — not INCOMPLETE/FAILED).
// ndpState classifies the gateway's NDP cache entry.
type ndpState int

const (
	ndpMissing   ndpState = iota // no usable entry.
	ndpStale                     // usable but unconfirmed (STALE/DELAY/PROBE); MAC may be outdated.
	ndpReachable                 // REACHABLE or PERMANENT.
)

// ndpLookup returns the gateway's MAC and the state of its NDP cache entry.
func ndpLookup(ifindex int, ip net.IP) (net.HardwareAddr, ndpState) {
	const (
		nudReachable uint16 = unix.NUD_REACHABLE | unix.NUD_PERMANENT
		nudUsable    uint16 = nudReachable | unix.NUD_STALE | unix.NUD_DELAY | unix.NUD_PROBE
	)

	raw, err := syscall.NetlinkRIB(unix.RTM_GETNEIGH, unix.AF_INET6)
	if err != nil {
		return nil, ndpMissing
	}

	msgs, err := syscall.ParseNetlinkMessage(raw)
	if err != nil {
		return nil, ndpMissing
	}

	for i := range msgs {
		m := &msgs[i]
		if m.Header.Type != uint16(unix.RTM_NEWNEIGH) || len(m.Data) < unix.SizeofNdMsg {
			continue
		}

		// struct ndmsg: family u8, pad u8, pad u16, ifindex i32, state u16, ...
		rawIfindex := binary.NativeEndian.Uint32(m.Data[4:8])
		ndmIfindex := int32(rawIfindex) //nolint:gosec // ifindex fits int32
		ndmState := binary.NativeEndian.Uint16(m.Data[8:10])

		if int(ndmIfindex) != ifindex || ndmState&nudUsable == 0 {
			continue
		}

		if mac, ok := neighMAC(m.Data[unix.SizeofNdMsg:], ip); ok {
			if ndmState&nudReachable != 0 {
				return mac, ndpReachable
			}

			return mac, ndpStale
		}
	}

	return nil, ndpMissing
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
