//go:build linux

package ping

import (
	"bytes"
	"context"
	"errors"
	"net"
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
// Here the send and receive happen synchronously on the calling goroutine (no hop), and
// the receive instant comes from the kernel's SO_TIMESTAMPNS software timestamp (stamped
// at RX softirq, before any scheduling) — the same TX=userspace-CLOCK_REALTIME /
// RX=kernel-software approach iputils' ping uses. A persistent socket would not help: it
// is opened before tSend, so its setup is never inside the measured RTT; Send stays
// stateless per-probe to avoid socket-lifecycle and concurrency complexity.
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

	ip, err := p.resolve(ctx)
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

	fd, err := openICMPSocket(fam, sockType, p.source)
	if err != nil {
		return failedResult
	}

	defer func() { _ = unix.Close(fd) }()

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

	dst, err := ipSockaddr(ip)
	if err != nil {
		return failedResult
	}

	tSend := time.Now()

	err = unix.Sendto(fd, wb, 0, dst)
	if err != nil {
		return failedResult
	}

	return pr.recv(fd, ip, tSend, deadline)
}

// resolve turns the configured address into an IP, honoring the resolve_family pin
// (network) the same way the pro-bing path does via SetNetwork.
func (p *icmpPinger) resolve(ctx context.Context) (net.IP, error) {
	if ip := net.ParseIP(p.addr); ip != nil {
		if p.network == networkIPv4 && ip.To4() == nil {
			return nil, errAddrFamily
		}

		if p.network == networkIPv6 && ip.To4() != nil {
			return nil, errAddrFamily
		}

		return ip, nil
	}

	netw := "ip"
	if p.network != "" {
		netw = p.network // "ip4"/"ip6".
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, netw, p.addr)
	if err != nil || len(ips) == 0 {
		return nil, errAddrFamily
	}

	return ips[0], nil
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

// openICMPSocket opens the ICMP socket for fam, with the given socket type (raw or
// datagram), enabling the kernel RX timestamp and the reply TTL cmsg. The recv timeout
// is armed per-read from the absolute deadline in recv, not here.
func openICMPSocket(fam icmpFamily, sockType int, source string) (int, error) {
	fd, err := unix.Socket(fam.domain, sockType|unix.SOCK_CLOEXEC, fam.proto)
	if err != nil {
		return -1, err
	}

	// RX kernel timestamp (software, CLOCK_REALTIME). Best-effort: if it cannot be set
	// the receive falls back to a userspace instant, degrading to synchronous accuracy.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1)

	// Reply TTL/hop-limit as a control message (captured, not displayed).
	_ = unix.SetsockoptInt(fd, fam.ttlLevel, fam.ttlRecvOpt, 1)

	if source != "" {
		err = bindSource(fd, source)
		if err != nil {
			_ = unix.Close(fd)

			return -1, err
		}
	}

	return fd, nil
}

// bindSource honors source= : a source IP binds the socket address; anything else is
// treated as an interface name (SO_BINDTODEVICE), matching the portable path's split.
func bindSource(fd int, source string) error {
	if srcIP := net.ParseIP(source); srcIP != nil {
		sa, err := ipSockaddr(srcIP)
		if err != nil {
			return err
		}

		return unix.Bind(fd, sa)
	}

	return unix.BindToDevice(fd, source)
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

// recv reads until this probe's echo reply arrives or the deadline passes, then computes
// RTT from the kernel RX timestamp (or a userspace fallback) minus tSend.
func (pr icmpProbe) recv(fd int, peer net.IP, tSend, deadline time.Time) Result {
	buf := make([]byte, recvBufSize)
	oob := make([]byte, controlBufSize)

	for {
		// Re-arm the recv timeout from the absolute deadline each iteration. SO_RCVTIMEO is
		// per-call, and a raw socket's fan-out delivers other hosts' (and other probes')
		// ICMP that we skip; without re-arming, each skipped packet would restart the full
		// timeout and the probe could spin past its deadline under continuous ICMP.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return failedResult
		}

		tv := unix.NsecToTimeval(int64(remaining))

		err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
		if err != nil {
			return failedResult
		}

		n, oobn, _, from, err := unix.Recvmsg(fd, buf, oob, 0)
		recvUser := time.Now()

		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue // the deadline is re-checked at the top of the loop.
			}

			return failedResult // SO_RCVTIMEO (EAGAIN) or another error: no reply.
		}

		data, ok := pr.payload(buf[:n])
		if !ok || !pr.match(data, from, peer) {
			continue
		}

		return pr.result(oob[:oobn], recvUser, tSend)
	}
}

// result turns a matched reply's ancillary data into a success Result: RTT from the
// kernel RX timestamp (or the userspace recvUser fallback) minus tSend, plus the TTL.
func (pr icmpProbe) result(oob []byte, recvUser, tSend time.Time) Result {
	rxTime, haveRx, ttl := pr.fam.parseControl(oob)
	if !haveRx {
		rxTime = recvUser
	}

	rtt := float64(rxTime.Sub(tSend).Microseconds()) / usPerMs
	if rtt < 0 {
		rtt = 0 // a clock step in the sub-ms window must not yield a negative RTT.
	}

	return Result{Success: true, Code: Success, RTT: rtt, TTL: ttl}
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

// parseControl extracts the kernel RX timestamp (SCM_TIMESTAMPNS) and reply TTL from the
// ancillary data. An absent/invalid timestamp leaves haveRx false so the caller falls
// back to a userspace instant; an absent TTL leaves ttl -1.
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
			len(c.Data) > 0 {
			ttl = int(c.Data[0])
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

// ipSockaddr builds a unix.Sockaddr for ip, choosing the family from ip itself.
func ipSockaddr(ip net.IP) (unix.Sockaddr, error) {
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

	return &unix.SockaddrInet6{Addr: a}, nil
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
