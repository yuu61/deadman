//go:build linux

package prober

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const (
	// controlBufSize bounds the ancillary (cmsg) buffer: ample for the RX timestamp.
	// Matches the reference STAMP implementation's 128.
	controlBufSize = 128
	// ipv4IHLMask is the low nibble of a raw IPv4 packet's first byte: the header length
	// in 32-bit words. A raw IPv4 socket prepends this header to each received datagram.
	ipv4IHLMask = 0x0f
	// ipv4IHLWordSize is the byte size of one IHL word (the IHL field counts 32-bit words).
	ipv4IHLWordSize = 4
	// nsPerSec bounds a valid nanosecond field when validating a kernel timestamp.
	nsPerSec = 1_000_000_000
)

// errAddrFamily reports that an address is not of the family the probe pinned/needs.
var errAddrFamily = errors.New("ping: address family mismatch")

// Send sends a native ICMP echo via a synchronous, kernel-timestamped probe.
//
// It replaces the portable pro-bing path (icmp_other.go) on Linux for one reason: RTT
// accuracy. pro-bing receives the echo reply on a dedicated goroutine, hands the packet
// to its run loop over a channel, and only then reads time.Now() as the receive instant
// — so every RTT carries the goroutine-wakeup + channel-hop latency, which dominates on
// a quiet LAN/loopback target and inflates AVG/JIT far above the true network RTT (MIN
// stays near the floor; AVG and JIT balloon).
//
// Here the send and receive happen synchronously on the calling goroutine, and the
// receive instant comes from the kernel's SO_TIMESTAMPNS software timestamp (stamped at
// RX softirq, before any scheduling) — the same TX=userspace-CLOCK_REALTIME /
// RX=kernel-software approach iputils' ping uses. The socket is nonblocking and driven
// through the Go runtime poller (os.File + RawConn), so a parked recv holds no OS thread
// (one blocked thread per in-flight probe would otherwise pile up across an async round
// during an outage). A persistent socket would not help accuracy: it is opened before
// tSend, so its setup is never inside the measured RTT; Send stays stateless per-probe.
//
// raw vs datagram follows useICMPPrivileged() exactly as the portable path does, so the
// behavior on root, non-root, and unprivileged LXC (where the datagram path is blocked
// and the raw path is used) is unchanged. Reply matching mirrors the next-hop prober
// (nexthop_ipv4.go): a raw ICMP socket sees every host's ICMP, so id+seq+token+src is
// load-bearing there; the datagram socket is demuxed by the kernel and rewrites our id,
// so it matches on seq+token+src only.
func (p *icmpPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, icmpTimeout)
	defer cancel()

	ip, zoneID, err := p.resolve(ctx)
	if err != nil {
		return probe.UnavailableResult()
	}

	deadline := probeDeadline(ctx)
	if time.Until(deadline) <= 0 {
		return probe.UnavailableResult()
	}

	fam := familyOf(ip)

	sockType := unix.SOCK_DGRAM
	if p.privileged {
		sockType = unix.SOCK_RAW
	}

	pr := icmpProbe{
		fam:        fam,
		privileged: p.privileged,
		id:         nextProbeID(),
		seq:        1,
		token:      newProbeToken(),
	}

	conn, err := dialICMP(fam, sockType, p.source, pr.id)
	if err != nil {
		return probe.UnavailableResult()
	}

	defer func() { discard(conn.f.Close()) }()

	dst, err := ipSockaddr(ip, zoneID)
	if err != nil {
		return probe.UnavailableResult()
	}

	wb, err := pr.build()
	if err != nil {
		return probe.UnavailableResult()
	}

	// One absolute deadline bounds both the send (a poller wait on a full buffer) and the
	// recv, so neither can park past the probe timeout.
	err = conn.f.SetDeadline(deadline)
	if err != nil {
		return probe.UnavailableResult()
	}

	// A ctx cancellation collapses the deadline so a parked send/recv unblocks at once,
	// honoring the ctx Send is given (matching pro-bing's RunWithContext; deadman's TUI
	// passes context.Background(), but a cancellable ctx is now respected).
	stopCancel := interruptOnDone(ctx, conn.f.SetDeadline)
	defer stopCancel()

	tSend, err := sendProbe(conn.rc, wb, conn.egressOOB, dst)
	if err != nil {
		return probe.UnavailableResult()
	}

	return pr.recv(conn.rc, ip, tSend)
}

// resolve turns the destination into an IP plus an IPv6 zone (scope) index. An address
// is already of the pinned family (Compile refuses one of another); a name resolves in the
// resolve_family pin (network) the same way the pro-bing path did. The resolver drops a
// zone, so only an address destination carries one.
func (p *icmpPinger) resolve(ctx context.Context) (net.IP, int, error) {
	if a, ok := p.dest.IP(); ok {
		ip, zone := literalAddr(a)

		return ip, zone, nil
	}

	netw := "ip"
	if p.network != "" {
		netw = p.network
	}

	name, _ := p.dest.Name()

	ips, err := net.DefaultResolver.LookupIP(ctx, netw, name)
	if err != nil || len(ips) == 0 {
		return nil, 0, errAddrFamily
	}

	return ips[0], 0, nil
}

// literalAddr is an address destination as the socket takes it: its bytes, and its IPv6
// zone (scope) as an interface index, 0 without one.
func literalAddr(a netip.Addr) (net.IP, int) {
	zone := 0
	if a.Zone() != "" {
		zone = zoneIndex(a.Zone())
	}

	return net.IP(a.AsSlice()), zone
}

// zoneIndex resolves an IPv6 zone (scope) to an interface index, accepting both a numeric
// scope id (fe80::1%2) and an interface name (fe80::1%eth0); 0 means "no zone".
func zoneIndex(zone string) int {
	// strconv.Atoi yields a 64-bit int; reject anything outside a valid (uint32) interface
	// index so an out-of-range zone (e.g. %-1 or %4294967296) cannot truncate into a wrong
	// ZoneId — it falls through to a no-zone (0) result instead.
	idx, err := strconv.Atoi(zone)
	if err == nil && idx >= 0 && idx <= math.MaxInt32 {
		return idx
	}

	ifi, err := net.InterfaceByName(zone)
	if err == nil {
		return ifi.Index
	}

	return 0
}

// icmpFamily captures the address-family-specific socket and parsing parameters, so the
// prober is written once without branching on a v6 flag. proto doubles as the socket
// protocol and the icmp.ParseMessage protocol number (ICMP=1, ICMPv6=58).
type icmpFamily struct {
	domain    int
	proto     int
	echoType  icmp.Type
	replyType icmp.Type
}

// familyOf returns the icmpFamily for ip's address family.
func familyOf(ip net.IP) icmpFamily {
	if ip.To4() != nil {
		return icmpFamily{
			domain:    unix.AF_INET,
			proto:     unix.IPPROTO_ICMP,
			echoType:  ipv4.ICMPTypeEcho,
			replyType: ipv4.ICMPTypeEchoReply,
		}
	}

	return icmpFamily{
		domain:    unix.AF_INET6,
		proto:     unix.IPPROTO_ICMPV6,
		echoType:  ipv6.ICMPTypeEchoRequest,
		replyType: ipv6.ICMPTypeEchoReply,
	}
}

// egressControl returns the per-send control message that pins the outgoing interface to
// ifIndex (IP_PKTINFO / IPV6_PKTINFO). This is privilege-free on every kernel, matching
// pro-bing's ControlMessage{IfIndex} — unlike SO_BINDTODEVICE, which the kernel gated on
// CAP_NET_RAW before 5.7. The source IP is left zero so the kernel auto-picks it to match
// the destination scope (documented under direct in docs/configuration.md).
func (f icmpFamily) egressControl(ifIndex int) []byte {
	if f.domain == unix.AF_INET {
		return (&ipv4.ControlMessage{IfIndex: ifIndex}).Marshal()
	}

	return (&ipv6.ControlMessage{IfIndex: ifIndex}).Marshal()
}

// icmpConn is the poller-registered socket a probe sends and receives on: the os.File
// (the caller closes it), its RawConn, and the per-send egress control message.
type icmpConn struct {
	f         *os.File
	rc        syscall.RawConn
	egressOOB []byte // IP_PKTINFO for an interface source; nil otherwise.
}

// dialICMP opens the poller-registered ICMP socket for fam and applies source=.
func dialICMP(fam icmpFamily, sockType int, source probe.Source, id int) (icmpConn, error) {
	fd, err := openICMPSocket(fam, sockType)
	if err != nil {
		return icmpConn{}, err
	}

	if sockType == unix.SOCK_RAW {
		err = filterICMPSocket(fd, fam.proto, id)
		if err != nil {
			_ = unix.Close(fd)

			return icmpConn{}, err
		}
	}

	egressOOB, err := applySource(fd, source, fam)
	if err != nil {
		_ = unix.Close(fd)

		return icmpConn{}, err
	}

	// os.NewFile registers the nonblocking socket with the runtime poller and takes
	// ownership of the fd, so from here it is closed via f.Close(), never unix.Close.
	f := os.NewFile(uintptr(fd), "icmp")

	rc, err := f.SyscallConn()
	if err != nil {
		discard(f.Close())

		return icmpConn{}, err
	}

	return icmpConn{f: f, rc: rc, egressOOB: egressOOB}, nil
}

// openICMPSocket opens a nonblocking ICMP socket for fam, with the given socket type (raw
// or datagram), enabling the kernel RX timestamp. The recv timeout
// is armed via the os.File read deadline in Send, not here.
func openICMPSocket(fam icmpFamily, sockType int) (int, error) {
	fd, err := unix.Socket(fam.domain, sockType|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, fam.proto)
	if err != nil {
		return -1, fmt.Errorf("open an ICMP socket: %w", err)
	}

	// RX kernel timestamp (software, CLOCK_REALTIME). Best-effort: if it cannot be set
	// the receive falls back to the monotonic span, degrading to synchronous accuracy.
	discard(unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1))

	return fd, nil
}

// applySource honors source= : a source address binds the socket (privilege-free for a
// local address; a zoned link-local one binds on its zone's interface); an interface
// yields a per-send IP_PKTINFO egress control message. The returned oob is nil unless an
// interface was given. Compile has matched an address's family to the probe's.
func applySource(fd int, source probe.Source, fam icmpFamily) ([]byte, error) {
	if a, ok := source.Addr(); ok {
		sa, err := addrSockaddr(a)
		if err != nil {
			return nil, err
		}

		err = unix.Bind(fd, sa)
		if err != nil {
			return nil, fmt.Errorf("bind the source address: %w", err)
		}

		return nil, nil
	}

	name, ok := source.Interface()
	if !ok {
		return nil, nil
	}

	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}

	return fam.egressControl(ifi.Index), nil
}

// sendProbe writes the echo through the runtime poller (no OS thread parked on a full send
// buffer) and returns the instant of the successful send. The send timestamp is taken
// inside the callback, right before the syscall, so a rare writability wait is excluded.
func sendProbe(rc syscall.RawConn, wb, oob []byte, dst unix.Sockaddr) (time.Time, error) {
	var (
		tSend time.Time
		serr  error
	)

	werr := rc.Write(func(fd uintptr) bool {
		tSend = time.Now()
		serr = unix.Sendmsg(int(fd), wb, oob, dst, 0)

		return !retryableSend(serr)
	})
	if werr != nil {
		return time.Time{}, werr
	}

	if serr != nil {
		return tSend, fmt.Errorf("send the ICMP echo: %w", serr)
	}

	return tSend, nil
}

// icmpProbe bundles a probe's family, privilege, and identity so the build/receive logic
// reads them as fields instead of threading boolean control parameters.
type icmpProbe struct {
	fam        icmpFamily
	privileged bool
	id, seq    int
	token      []byte
}

// build marshals the echo request. icmp.Message.Marshal computes the ICMPv4 checksum;
// for ICMPv6 it leaves the checksum zero and the kernel fills it in.
func (pr icmpProbe) build() ([]byte, error) {
	msg := icmp.Message{
		Type: pr.fam.echoType,
		Code: 0,
		Body: &icmp.Echo{ID: pr.id, Seq: pr.seq, Data: pr.token},
	}

	wire, err := msg.Marshal(nil)
	if err != nil {
		return nil, fmt.Errorf("marshal the ICMP echo: %w", err)
	}

	return wire, nil
}

// recv reads through the runtime poller until this probe's echo reply arrives or the
// os.File read deadline passes, skipping foreign ICMP (raw fan-out). A parked read holds
// no OS thread, and the absolute deadline bounds the loop even under continuous ICMP.
func (pr icmpProbe) recv(rc syscall.RawConn, peer net.IP, tSend time.Time) probe.Result {
	buf := make([]byte, recvBufSize)
	oob := make([]byte, controlBufSize)

	for {
		var (
			bufn, oobn int
			from       unix.Sockaddr
			rerr       error
		)

		readErr := rc.Read(func(fd uintptr) bool {
			bufn, oobn, _, from, rerr = unix.Recvmsg(int(fd), buf, oob, 0)

			return !retryable(rerr)
		})

		recvUser := time.Now()

		if readErr != nil || rerr != nil {
			if errors.Is(readErr, os.ErrDeadlineExceeded) {
				return probe.FailedResult()
			}

			return probe.UnavailableResult()
		}

		data, ok := pr.payload(buf[:bufn])
		if !ok || !pr.match(data, from, peer) {
			continue
		}

		return replyResult(oob[:oobn], recvUser, tSend)
	}
}

// retryable reports whether a send/recv syscall error means "wait for the fd to be ready
// and try again" rather than fail: EAGAIN/EWOULDBLOCK (the poller re-arms) or an
// interrupted syscall (EINTR). Shared by sendProbe and recv so both classify identically.
func retryable(err error) bool {
	return errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) ||
		errors.Is(err, unix.EINTR)
}

// retryableSend extends retryable with ENOBUFS — transient output-queue congestion (a full
// qdisc/txqueue), which pro-bing retried rather than scoring as a down host. Bounded by the
// write deadline, so sustained congestion still ends as a timeout failure.
func retryableSend(err error) bool {
	return retryable(err) || errors.Is(err, unix.ENOBUFS)
}

// payload returns the ICMP message bytes from a received datagram. A raw IPv4 socket
// delivers the leading IP header (which must be skipped); raw IPv6 and both datagram
// paths deliver the ICMP message directly.
func (pr icmpProbe) payload(b []byte) ([]byte, bool) {
	if pr.privileged && pr.fam.domain == unix.AF_INET {
		if len(b) < 1 {
			return nil, false
		}

		ihl := int(b[0]&ipv4IHLMask) * ipv4IHLWordSize
		if ihl <= 0 || len(b) < ihl {
			return nil, false
		}

		return b[ihl:], true
	}

	return b, true
}

// match reports whether data is this probe's echo reply. On the raw path the id is
// load-bearing (the socket sees all ICMP); on the datagram path the kernel rewrote our
// id, so seq+token (and the per-socket demux) carry it. The source is rejected only on a
// positive mismatch, so a missing from-address does not drop an otherwise-valid reply.
func (pr icmpProbe) match(data []byte, from unix.Sockaddr, peer net.IP) bool {
	if fip := sockaddrIP(from); fip != nil && !fip.Equal(peer) {
		return false
	}

	msg, err := icmp.ParseMessage(pr.fam.proto, data)
	if err != nil || msg.Type != pr.fam.replyType {
		return false
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		return false
	}

	if pr.privileged && echo.ID != pr.id {
		return false
	}

	return echo.Seq == pr.seq && bytes.Equal(echo.Data, pr.token)
}

// replyResult turns a matched reply into a success probe.Result. RTT prefers the kernel RX timestamp
// (precise) but falls back to the monotonic send->recv span when the kernel value is
// implausible — i.e. a CLOCK_REALTIME step between send and receive moved it outside
// [0, rttMono]. recvUser carries a monotonic reading, so rttMono is step-immune and is a
// valid upper bound (the kernel stamps RX before the userspace Recvmsg returns). k==0 is
// accepted: a genuine sub-microsecond reply truncates to 0 ms.
func replyResult(oob []byte, recvUser, tSend time.Time) probe.Result {
	rttMono := recvUser.Sub(tSend)
	dur := rttMono

	rxTime, haveRx := rxTimestamp(oob)
	if haveRx {
		if k := rxTime.Sub(tSend); k >= 0 && k <= rttMono {
			dur = k
		}
	}

	rtt := float64(dur.Microseconds()) / usPerMs

	return probe.SuccessResult(rtt)
}

// rxTimestamp extracts the kernel RX timestamp (SCM_TIMESTAMPNS) from the ancillary data.
// An absent/invalid timestamp reports false so the caller falls back to the monotonic
// span.
func rxTimestamp(oob []byte) (time.Time, bool) {
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return time.Time{}, false
	}

	for i := range cmsgs {
		c := &cmsgs[i]

		if c.Header.Level == unix.SOL_SOCKET && c.Header.Type == unix.SCM_TIMESTAMPNS {
			if t, ok := timespecToTime(c.Data); ok {
				return t, true
			}
		}
	}

	return time.Time{}, false
}

// Sizes of an SCM_TIMESTAMPNS payload: a struct timespec of two longs, which are 64-bit
// words on a 64-bit kernel ABI and 32-bit ones on a 32-bit one (386, arm).
const (
	timespec64Size = 16
	timespec32Size = 8
)

// timespecToTime decodes an SCM_TIMESTAMPNS cmsg payload, rejecting an out-of-range or
// zero timestamp (validation borrowed from the reference STAMP implementation), and one
// before 1970, which no realtime receive stamp is. The payload's length tells its
// layout, so the two words are read in place, without reflection or an allocation on the
// probe's path.
func timespecToTime(b []byte) (time.Time, bool) {
	var sec, nsec uint64

	switch len(b) {
	case timespec64Size:
		ts := [timespec64Size]byte(b)
		sec, nsec = binary.NativeEndian.Uint64(ts[:8]), binary.NativeEndian.Uint64(ts[8:])
	case timespec32Size:
		ts := [timespec32Size]byte(b)

		sec32 := binary.NativeEndian.Uint32(ts[:4])
		if sec32 > math.MaxInt32 {
			return time.Time{}, false // a negative 32-bit time_t.
		}

		sec, nsec = uint64(sec32), uint64(binary.NativeEndian.Uint32(ts[4:]))
	default:
		return time.Time{}, false
	}

	if sec == 0 && nsec == 0 || sec > math.MaxInt64 || nsec >= nsPerSec {
		return time.Time{}, false
	}

	return time.Unix(int64(sec), int64(nsec)), true
}

// ipSockaddr builds a unix.Sockaddr for ip, choosing the family from ip itself and setting
// the IPv6 scope (zone) id when one is given.
func ipSockaddr(ip net.IP, zoneID int) (unix.Sockaddr, error) {
	if ip4 := ip.To4(); ip4 != nil {
		var a [4]byte

		copy(a[:], ip4)

		return &unix.SockaddrInet4{Addr: a}, nil
	}

	ip16 := ip.To16()
	if ip16 == nil {
		return nil, errAddrFamily
	}

	var a [16]byte

	copy(a[:], ip16)

	sa := &unix.SockaddrInet6{Addr: a}
	// Bounded narrowing: a zoneID outside the uint32 interface-index range leaves ZoneId 0
	// (no zone) rather than truncating. zoneIndex already filters numeric zones, so this is
	// belt-and-suspenders that also keeps the int->uint32 conversion provably safe here.
	if zoneID >= 0 && zoneID <= math.MaxInt32 {
		sa.ZoneId = uint32(zoneID)
	}

	return sa, nil
}

// addrSockaddr builds the sockaddr of a, a zone naming its interface as the IPv6 scope.
func addrSockaddr(a netip.Addr) (unix.Sockaddr, error) {
	zone := 0
	if a.Zone() != "" {
		zone = zoneIndex(a.Zone())
	}

	return ipSockaddr(net.IP(a.AsSlice()), zone)
}

// sockaddrIP extracts the IP from a recvmsg source address, or nil if unknown.
func sockaddrIP(sa unix.Sockaddr) net.IP {
	switch v := sa.(type) {
	case *unix.SockaddrInet4:
		return net.IP(v.Addr[:])
	case *unix.SockaddrInet6:
		return net.IP(v.Addr[:])
	default:
		return nil
	}
}
