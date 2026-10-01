package prober

import (
	"context"
	"crypto/rand"
	"math"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const icmpTimeout = 1 * time.Second

// icmpPinger sends a native direct ICMP echo; how is each platform's Send
// (icmp_linux.go, icmp_other.go). The probe.Direct->pinger wiring test
// (resolve_family_test.go) holds on both.
type icmpPinger struct {
	dest       probe.Destination
	source     probe.Source
	network    string // "ip4"/"ip6" to pin the resolve family (resolve_family=), "" for auto.
	privileged bool
}

func newICMPPinger(dest probe.Destination, d probe.Direct) (probe.Pinger, error) {
	return &icmpPinger{
		dest:       dest,
		source:     d.Source,
		network:    resolveNetwork(d.Family),
		privileged: useICMPPrivileged(),
	}, nil
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
