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
	"time"

	"golang.org/x/sys/unix"
)

// AF_PACKET-based link transport. Sending a frame whose L2 destination is the
// gateway's MAC is what forces the next-hop; the kernel adds the Ethernet header
// for SOCK_DGRAM, so we hand it the L3 packet and the destination MAC.

const (
	// arpResolveTimeout bounds how long resolveGateway waits for the kernel to
	// populate the neighbor cache after a nudge.
	arpResolveTimeout = 500 * time.Millisecond
	// arpPollInterval is the neighbor-cache re-read cadence.
	arpPollInterval = 50 * time.Millisecond
	// atfCom is the /proc/net/arp flag bit for a complete (resolved) entry.
	atfCom = 0x2
	// arpKickPort is the (discard) UDP port we write to so the kernel ARPs the
	// gateway; the datagram itself is irrelevant.
	arpKickPort = "9"
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

// resolveGateway reads the gateway's MAC from the kernel neighbor cache, nudging
// the kernel to resolve it first if the entry is missing. Reading the cache (and
// letting the kernel's own ARP state machine own resolution) avoids racing it with
// hand-rolled ARP.
func (afpacketTransport) resolveGateway(
	iface *net.Interface,
	nexthop net.IP,
) (net.HardwareAddr, error) {
	target := nexthop.String()

	if mac, ok := arpLookup(iface.Name, target); ok {
		return mac, nil
	}

	// connect() alone does not trigger ARP; an actual write to the on-link gateway
	// makes the kernel resolve it. Then re-read the cache until it lands.
	kickARP(nexthop)

	deadline := time.Now().Add(arpResolveTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(arpPollInterval)

		if mac, ok := arpLookup(iface.Name, target); ok {
			return mac, nil
		}
	}

	return nil, fmt.Errorf("nexthop: cannot resolve MAC for %s on %s", target, iface.Name)
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

// kickARP nudges the kernel to resolve gw by writing a datagram toward it. Errors
// are ignored: the only purpose is the side effect of triggering resolution.
func kickARP(gw net.IP) {
	d := net.Dialer{Timeout: arpPollInterval}

	c, err := d.DialContext(context.Background(), "udp", net.JoinHostPort(gw.String(), arpKickPort))
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	_, _ = c.Write([]byte{0})
}
