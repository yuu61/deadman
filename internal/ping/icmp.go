package ping

import (
	"context"
	"net"
	"runtime"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

const icmpTimeout = 1 * time.Second

// icmpPinger sends a native ICMP echo using pro-bing, replacing the original's
// shell-out to `ping -c 1`. Native ICMP is portable (Windows/Linux/macOS) and
// avoids OS-specific output parsing.
type icmpPinger struct {
	addr       string
	source     string
	privileged bool
}

func newICMPPinger(s Spec) (Pinger, error) {
	return &icmpPinger{
		addr:   s.Addr,
		source: s.Source,
		// Windows requires privileged mode (no admin elevation needed); on
		// Linux/macOS the unprivileged UDP path is used by default.
		privileged: runtime.GOOS == "windows",
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
		// The original `ping -I` accepted either a source IP or (on Linux) an
		// interface name. pro-bing's Source binds by IP, InterfaceName by name.
		if net.ParseIP(p.source) != nil {
			pinger.Source = p.source
		} else {
			pinger.InterfaceName = p.source
		}
	}

	if err := pinger.RunWithContext(ctx); err != nil {
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
			RTT:     float64(st.AvgRtt.Microseconds()) / 1000.0,
			TTL:     ttl,
		}
	}
	return Result{Code: Failed, TTL: -1}
}
