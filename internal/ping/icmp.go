package ping

import (
	"context"
	"net"
	"runtime"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

const icmpTimeout = 1 * time.Second

// usPerMs converts microseconds to milliseconds.
const usPerMs = 1000.0

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

// icmpPinger sends a native ICMP echo using pro-bing. Native ICMP is portable
// (Windows/Linux/macOS) and avoids shelling out to `ping -c 1` and parsing
// OS-specific output.
type icmpPinger struct {
	addr       string
	source     string
	privileged bool
}

func newICMPPinger(s Spec) (Pinger, error) {
	return &icmpPinger{
		addr:       s.Addr,
		source:     s.Source,
		privileged: useICMPPrivileged(),
	}, nil
}

func (p *icmpPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, icmpTimeout)
	defer cancel()

	pinger, err := probing.NewPinger(p.addr)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	pinger.SetPrivileged(p.privileged)
	pinger.Count = 1
	pinger.Timeout = icmpTimeout
	pinger.RecordTTLs = true

	if p.source != "" {
		// A source may be a source IP or (on Linux) an interface name:
		// pro-bing's Source binds by IP, InterfaceName by name.
		if net.ParseIP(p.source) != nil {
			pinger.Source = p.source
		} else {
			pinger.InterfaceName = p.source
		}
	}

	err = pinger.RunWithContext(ctx)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	st := pinger.Statistics()
	if st.PacketsRecv > 0 {
		ttl := -1
		if len(st.TTLs) > 0 {
			ttl = int(st.TTLs[0])
		}

		return Result{
			Success: true,
			Code:    Success,
			RTT:     float64(st.AvgRtt.Microseconds()) / usPerMs,
			TTL:     ttl,
		}
	}

	return Result{Code: Failed, TTL: -1}
}
