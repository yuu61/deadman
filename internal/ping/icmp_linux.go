//go:build linux

package ping

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/icmp"
	netipv4 "golang.org/x/net/ipv4" // aliased: "ipv4" clashes with the IP-version constant in osname.go.
	netipv6 "golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const (
	// controlBufSize bounds the ancillary (cmsg) buffer: enough for the RX timestamp
	// plus the TTL/hop-limit cmsg. Matches the reference STAMP implementation's 128.
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

// failedResult is the canonical "no reply / could not probe" outcome.
var failedResult = Result{Code: Failed, TTL: -1}

// icmpPinger sends a native ICMP echo via a synchronous, kernel-timestamped probe.
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
//
// The type name and fields match the portable icmpPinger (icmp_other.go) so the
// Spec->pinger wiring test (resolve_family_test.go) holds on both platforms.
type icmpPinger struct {
	addr       string
	source     string
	network    string // "ip4"/"ip6" to pin the resolve family (resolve_family=), "" for auto.
	privileged bool
}

func newICMPPinger(s Spec) (Pinger, error) {
	return &icmpPinger{
		addr:       s.Addr,
		source:     s.Source,
		network:    resolveNetwork(s),
		privileged: useICMPPrivileged(),
	}, nil
}

func (p *icmpPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, icmpTimeout)
	defer cancel()

	ip, zoneID, err := p.resolve(ctx)
	if err != nil {
		return failedResult
	}

	deadline := probeDeadline(ctx)
	if time.Until(deadline) <= 0 {
		return failedResult
	}

	fam := familyOf(ip)

	sockType := unix.SOCK_DGRAM
	if p.privileged {
		sockType = unix.SOCK_RAW
	}

	c, err := dialICMP(fam, sockType, p.source)
	if err != nil {
		return failedResult
	}

	defer func() { _ = c.f.Close() }()

	dst, err := ipSockaddr(ip, zoneID)
	if err != nil {
		return failedResult
	}

	pr := icmpProbe{
		fam:        fam,
		privileged: p.privileged,
		id:         nextProbeID(),
		seq:        1,
		token:      newProbeToken(),
	}

	wb, err := pr.build()
	if err != nil {
		return failedResult
	}

	// One absolute deadline bounds both the send (a poller wait on a full buffer) and the
	// recv, so neither can park past the probe timeout.
	err = c.f.SetDeadline(deadline)
	if err != nil {
		return failedResult
	}

	// A ctx cancellation collapses the deadline so a parked send/recv unblocks at once,
	// honoring the ctx Send is given (matching pro-bing's RunWithContext; deadman's TUI
	// passes context.Background(), but a cancellable ctx is now respected).
	stopCancel := context.AfterFunc(ctx, func() { _ = c.f.SetDeadline(time.Now()) })
	defer stopCancel()

	tSend, err := sendProbe(c.rc, wb, c.egressOOB, dst)
	if err != nil {
		return failedResult
	}

	return pr.recv(c.rc, ip, tSend)
}

// resolve turns the configured address into an IP plus an IPv6 zone (scope) index,
// honoring the resolve_family pin (network) the same way the pro-bing path did. netip is
// used for literals because, unlike net.ParseIP, it accepts the fe80::1%zone form and
// exposes the zone — net.DefaultResolver.LookupIP (the hostname path) drops it.
func (p *icmpPinger) resolve(ctx context.Context) (net.IP, int, error) {
	a, perr := netip.ParseAddr(p.addr)
	if perr == nil {
		return p.literalAddr(a)
	}

	netw := "ip"
	if p.network != "" {
		netw = p.network // "ip4"/"ip6".
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, netw, p.addr)
	if err != nil || len(ips) == 0 {
		return nil, 0, errAddrFamily
	}

	return ips[0], 0, nil
}

// literalAddr resolves an IP literal, enforcing the resolve_family pin and extracting an
// IPv6 zone (scope) if present.
func (p *icmpPinger) literalAddr(a netip.Addr) (net.IP, int, error) {
	a = a.Unmap()

	if p.network == networkIPv4 && a.Is6() {
		return nil, 0, errAddrFamily
	}

	if p.network == networkIPv6 && a.Is4() {
		return nil, 0, errAddrFamily
	}

	zone := 0
	if a.Is6() && a.Zone() != "" {
		zone = zoneIndex(a.Zone())
	}

	return net.IP(a.AsSlice()), zone, nil
}

// zoneIndex resolves an IPv6 zone (scope) to an interface index, accepting both a numeric
// scope id (fe80::1%2) and an interface name (fe80::1%eth0); 0 means "no zone".
func zoneIndex(zone string) int {
	idx, err := strconv.Atoi(zone)
	if err == nil {
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
// protocol and the icmp.ParseMessage protocol number (ICMP=1, ICMPv6=58). ttlRecvOpt is
// the option that requests the TTL cmsg; ttlCmsgType is the (different) type the cmsg
// then arrives as — IP_RECVTTL enables an IP_TTL cmsg, IPV6_RECVHOPLIMIT an IPV6_HOPLIMIT.
type icmpFamily struct {
	domain      int
	proto       int
	echoType    icmp.Type
	replyType   icmp.Type
	ttlLevel    int
	ttlRecvOpt  int
	ttlCmsgType int
}

// familyOf returns the icmpFamily for ip's address family.
func familyOf(ip net.IP) icmpFamily {
	if ip.To4() != nil {
		return icmpFamily{
			domain:      unix.AF_INET,
			proto:       unix.IPPROTO_ICMP,
			echoType:    netipv4.ICMPTypeEcho,
			replyType:   netipv4.ICMPTypeEchoReply,
			ttlLevel:    unix.IPPROTO_IP,
			ttlRecvOpt:  unix.IP_RECVTTL,
			ttlCmsgType: unix.IP_TTL,
		}
	}

	return icmpFamily{
		domain:      unix.AF_INET6,
		proto:       unix.IPPROTO_ICMPV6,
		echoType:    netipv6.ICMPTypeEchoRequest,
		replyType:   netipv6.ICMPTypeEchoReply,
		ttlLevel:    unix.IPPROTO_IPV6,
		ttlRecvOpt:  unix.IPV6_RECVHOPLIMIT,
		ttlCmsgType: unix.IPV6_HOPLIMIT,
	}
}

// egressControl returns the per-send control message that pins the outgoing interface to
// ifIndex (IP_PKTINFO / IPV6_PKTINFO). This is privilege-free on every kernel, matching
// pro-bing's ControlMessage{IfIndex} — unlike SO_BINDTODEVICE, which the kernel gated on
// CAP_NET_RAW before 5.7. The source IP is left zero so the kernel auto-picks it to match
// the destination scope (README: "送信元 IP は宛先のスコープに合わせて自動選択").
func (fam icmpFamily) egressControl(ifIndex int) []byte {
	if fam.domain == unix.AF_INET {
		return (&netipv4.ControlMessage{IfIndex: ifIndex}).Marshal()
	}

	return (&netipv6.ControlMessage{IfIndex: ifIndex}).Marshal()
}

// icmpConn is the poller-registered socket a probe sends and receives on: the os.File
// (the caller closes it), its RawConn, and the per-send egress control message.
type icmpConn struct {
	f         *os.File
	rc        syscall.RawConn
	egressOOB []byte // IP_PKTINFO for an interface source; nil otherwise.
}

// dialICMP opens the poller-registered ICMP socket for fam and applies source=.
func dialICMP(fam icmpFamily, sockType int, source string) (icmpConn, error) {
	fd, err := openICMPSocket(fam, sockType)
	if err != nil {
		return icmpConn{}, err
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
		_ = f.Close()

		return icmpConn{}, err
	}

	return icmpConn{f: f, rc: rc, egressOOB: egressOOB}, nil
}

// openICMPSocket opens a nonblocking ICMP socket for fam, with the given socket type (raw
// or datagram), enabling the kernel RX timestamp and the reply TTL cmsg. The recv timeout
// is armed via the os.File read deadline in Send, not here.
func openICMPSocket(fam icmpFamily, sockType int) (int, error) {
	fd, err := unix.Socket(fam.domain, sockType|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, fam.proto)
	if err != nil {
		return -1, err
	}

	// RX kernel timestamp (software, CLOCK_REALTIME). Best-effort: if it cannot be set
	// the receive falls back to the monotonic span, degrading to synchronous accuracy.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1)

	// Reply TTL/hop-limit as a control message (captured, not displayed).
	_ = unix.SetsockoptInt(fd, fam.ttlLevel, fam.ttlRecvOpt, 1)

	// We deliberately do NOT attach a kernel BPF id-filter on the raw path the way
	// pro-bing's InstallICMPIDFilter does. Correctness already lives in the userspace match
	// (id+seq+token+src), so a filter would only be a scale optimization: dropping foreign
	// ICMP in-kernel to spare CPU and rcvbuf when root+many-targets+async fan-out every
	// host's ICMP onto each socket. That degradation is visible and load-correlated (and
	// the in-repo nexthop raw path is unfiltered too), whereas a subtly wrong BPF that the
	// kernel still accepts would silently drop every probe's own reply — a monitor that
	// reports everything down. Not shipped because it can't be validated here (needs root)
	// and is catastrophic if wrong. A future root-tested change could add SO_ATTACH_FILTER
	// mirroring pro-bing's utils_linux.go if high-N raw load ever demands it.

	return fd, nil
}

// applySource honors source= : a source IP binds the socket (privilege-free for a local
// address); an interface name yields a per-send IP_PKTINFO egress control message. The
// returned oob is nil unless an interface name was given.
func applySource(fd int, source string, fam icmpFamily) ([]byte, error) {
	if source == "" {
		return nil, nil
	}

	if srcIP := net.ParseIP(source); srcIP != nil {
		sa, err := ipSockaddr(srcIP, 0)
		if err != nil {
			return nil, err
		}

		return nil, unix.Bind(fd, sa)
	}

	ifi, err := net.InterfaceByName(source)
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

	return tSend, serr
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

	return msg.Marshal(nil)
}

// recv reads through the runtime poller until this probe's echo reply arrives or the
// os.File read deadline passes, skipping foreign ICMP (raw fan-out). A parked read holds
// no OS thread, and the absolute deadline bounds the loop even under continuous ICMP.
func (pr icmpProbe) recv(rc syscall.RawConn, peer net.IP, tSend time.Time) Result {
	buf := make([]byte, recvBufSize)
	oob := make([]byte, controlBufSize)

	for {
		var (
			n, oobn int
			from    unix.Sockaddr
			rerr    error
		)

		readErr := rc.Read(func(fd uintptr) bool {
			n, oobn, _, from, rerr = unix.Recvmsg(int(fd), buf, oob, 0)

			return !retryable(rerr)
		})

		recvUser := time.Now()

		if readErr != nil || rerr != nil {
			return failedResult // deadline exceeded, poller error, or recvmsg error.
		}

		data, ok := pr.payload(buf[:n])
		if !ok || !pr.match(data, from, peer) {
			continue
		}

		return pr.result(oob[:oobn], recvUser, tSend)
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

// result turns a matched reply into a success Result. RTT prefers the kernel RX timestamp
// (precise) but falls back to the monotonic send->recv span when the kernel value is
// implausible — i.e. a CLOCK_REALTIME step between send and receive moved it outside
// [0, rttMono]. recvUser carries a monotonic reading, so rttMono is step-immune and is a
// valid upper bound (the kernel stamps RX before the userspace Recvmsg returns). k==0 is
// accepted: a genuine sub-microsecond reply truncates to 0 ms.
func (pr icmpProbe) result(oob []byte, recvUser, tSend time.Time) Result {
	rttMono := recvUser.Sub(tSend)
	dur := rttMono

	rxTime, haveRx, ttl := pr.fam.parseControl(oob)
	if haveRx {
		if k := rxTime.Sub(tSend); k >= 0 && k <= rttMono {
			dur = k
		}
	}

	rtt := float64(dur.Microseconds()) / usPerMs

	return Result{Success: true, Code: Success, RTT: rtt, TTL: ttl}
}

// parseControl extracts the kernel RX timestamp (SCM_TIMESTAMPNS) and reply TTL from the
// ancillary data. An absent/invalid timestamp leaves haveRx false so the caller falls
// back to the monotonic span; an absent TTL leaves ttl -1.
func (fam icmpFamily) parseControl(oob []byte) (time.Time, bool, int) {
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return time.Time{}, false, -1
	}

	var (
		rxTime time.Time
		haveRx bool
	)

	ttl := -1

	for i := range cmsgs {
		c := &cmsgs[i]

		if c.Header.Level == unix.SOL_SOCKET && c.Header.Type == unix.SCM_TIMESTAMPNS {
			if t, ok := timespecToTime(c.Data); ok {
				rxTime, haveRx = t, true
			}

			continue
		}

		if int(c.Header.Level) == fam.ttlLevel && int(c.Header.Type) == fam.ttlCmsgType &&
			len(c.Data) >= 4 {
			// The IP_TTL / IPV6_HOPLIMIT cmsg is a native-endian C int; reading c.Data[0]
			// would yield 0 on a big-endian host (s390x/ppc64). All shipped arches are LE,
			// but NativeEndian keeps it correct everywhere.
			ttl = int(binary.NativeEndian.Uint32(c.Data))
		}
	}

	return rxTime, haveRx, ttl
}

// timespecToTime decodes an SCM_TIMESTAMPNS cmsg payload, rejecting an out-of-range or
// zero timestamp (validation borrowed from the reference STAMP implementation).
func timespecToTime(b []byte) (time.Time, bool) {
	if len(b) < int(unsafe.Sizeof(unix.Timespec{})) {
		return time.Time{}, false
	}

	ts := *(*unix.Timespec)(unsafe.Pointer(&b[0])) //nolint:gosec // decode a fixed-layout kernel cmsg payload.
	if ts.Sec == 0 && ts.Nsec == 0 {
		return time.Time{}, false
	}

	if ts.Nsec < 0 || ts.Nsec >= nsPerSec {
		return time.Time{}, false
	}

	return time.Unix(0, unix.TimespecToNsec(ts)), true
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
	sa.ZoneId = uint32(zoneID) //nolint:gosec // ifindex is non-negative.

	return sa, nil
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
