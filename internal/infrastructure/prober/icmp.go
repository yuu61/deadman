package prober

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// icmpTimeout is how long a direct or forced echo waits for its reply, counted from when
// it is sent, as ping's -W counts it. resolveTimeout bounds the name lookup before it on
// its own budget, so a slow resolver never shortens the wait into a loss the target did
// not cause; a lookup that runs out observed nothing. (The forced probe's gateway lookup
// has its own bound, neighborResolveTimeout.)
const (
	icmpTimeout    = 1 * time.Second
	resolveTimeout = 1 * time.Second
)

// icmpPinger sends a native direct ICMP echo; how is each platform's Send
// (icmp_linux.go, icmp_other.go). The probe.Direct->pinger wiring test
// (resolve_family_test.go) holds on both.
type icmpPinger struct {
	dest       probe.Destination
	source     probe.Source
	network    string // "ip4"/"ip6" to pin the resolve family (resolve_family=), "" for auto.
	privileged bool
	echo       probe.EchoOptions
}

func newICMPPinger(dest probe.Destination, d probe.Direct) (probe.Pinger, error) {
	if d.Echo.DF && !nativeDFSupported {
		return nil, fmt.Errorf("direct df is not supported on %s", runtime.GOOS)
	}

	return &icmpPinger{
		dest:       dest,
		source:     d.Source,
		network:    resolveNetwork(d.Family),
		privileged: useICMPPrivileged(),
		echo:       d.Echo,
	}, nil
}

// echoPayload retains a random token at the start of the requested payload.
func echoPayload(size int) []byte {
	payload := make([]byte, size)
	copy(payload, newProbeToken())

	return payload
}

// echoReceiveSize leaves room for a maximum IPv4 header and the ICMP echo header.
func echoReceiveSize(payload int) int {
	const headerRoom = 68

	return max(recvBufSize, payload+headerRoom)
}

// bsdRawSpace is the smallest default send and receive space of a BSD raw ICMP socket:
// RIPSNDQ and RIPRCVQ are 8192 bytes in Darwin, OpenBSD, NetBSD and DragonFly (netinet/raw_ip.c),
// and RIPV6SNDQ is 8192 in OpenBSD and NetBSD. FreeBSD's rip_sendspace is 9216. An echo that
// does not fit is refused on send or dropped on receive, and pro-bing cannot grow the space.
const bsdRawSpace = 8192

// needsNativeEcho reports whether a direct row needs the native echo path instead of the
// default one (pro-bing, or sendScoped for a zoned source): on a BSD, an echo too big for a
// raw socket's default space. A do-not-fragment echo is sent only by Linux's own Send.
func needsNativeEcho(echo probe.EchoOptions, bsd bool) bool {
	return bsd && echoReceiveSize(echo.Size.Bytes()) > bsdRawSpace
}

// echoSocketBufferSize also reserves kernel address/control data and room for a
// request and reply to be queued together on an unfiltered raw loopback socket.
func echoSocketBufferSize(payload int) int {
	return 2 * echoReceiveSize(payload)
}

// probeCounter hands out per-probe ICMP IDs. Under the raw-socket fan-out every
// listener sees every host's ICMP, so each in-flight probe needs a distinct id to
// disambiguate its own reply (together with the peer address).
var probeCounter atomic.Int32

// nextProbeID returns the next 16-bit ICMP id: the counter's low 16 bits, which wrap
// with it.
func nextProbeID() int {
	return int(probeCounter.Add(1)) & math.MaxUint16
}

// probeTokenLen is the size of the random per-probe token echoed in the ICMP data.
const probeTokenLen = 8

// newProbeToken returns a random token placed in the echo payload and verified in
// the reply. Because a raw ICMP socket also receives other processes' replies
// (whose ids/seqs collide with ours), the token is what distinguishes our probe
// from another deadman watching the same destination. It is not security-sensitive.
func newProbeToken() []byte {
	b := make([]byte, probeTokenLen)
	// crypto/rand.Read does not fail on supported platforms; a zero token still works.
	_, _ = rand.Read(b)

	return b
}

// probeDeadline is the earlier of the per-probe timeout and any caller deadline.
func probeDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(icmpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return d
	}

	return deadline
}

// useICMPPrivileged reports, once per process, whether native ICMP should use the
// privileged raw-socket path.
//
// Windows always requires the raw path (no admin elevation needed). On Unix we
// probe whether a raw ICMP socket can actually be opened — i.e. the process is
// root or carries CAP_NET_RAW (e.g. via `setcap cap_net_raw+ep`). When it can, we
// prefer the raw path, because the unprivileged datagram path (SOCK_DGRAM ICMP)
// is additionally gated by net.ipv4.ping_group_range: that range excludes root by
// default and, in locked-down environments such as an unprivileged LXC container,
// cannot even be widened — so a root deadman that only tried the datagram path
// would fail every probe. When the raw probe fails we fall back to the
// unprivileged path, which is what macOS and an unprivileged Linux user with a
// configured ping_group_range expect.
var useICMPPrivileged = sync.OnceValue(func() bool {
	if runtime.GOOS == "windows" {
		return true
	}

	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
})

// directICMPAvailable reports whether any native direct-ICMP path can open a socket
// on this host: the privileged raw path (root/CAP_NET_RAW) or the unprivileged
// datagram path (SOCK_DGRAM ICMP) used when not privileged. The datagram path needs
// the caller's gid inside net.ipv4.ping_group_range. When both fail — the common
// non-root case where that range is empty or excludes the user (e.g. WSL2's "1 0"
// default) — every direct probe fails with no packet egress, and a startup warning
// surfaces it (see Host). The raw probe alone is not enough to decide this: it is
// false on macOS and on a correctly configured non-root Linux where the datagram path
// works fine, so we must actually attempt both. The raw answer is fixed for the run
// (capabilities are set at exec), but ping_group_range can be changed while deadman
// runs, so the datagram socket is tried on every ask: the warnings rebuilt by a reload
// then follow the fix. The udp4 probe stands in for both families: ping_group_range
// gates ICMPv6 datagram sockets too.
func directICMPAvailable() bool {
	if useICMPPrivileged() {
		return true // raw path opens (or Windows, which always uses raw).
	}

	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}
