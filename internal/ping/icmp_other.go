//go:build !linux

package ping

import (
	"context"
	"net"

	probing "github.com/prometheus-community/pro-bing"
)

// icmpPinger sends a native ICMP echo using pro-bing. Native ICMP is portable
// (Windows/macOS/*BSD) and avoids shelling out to `ping -c 1` and parsing
// OS-specific output. Linux replaces this with a synchronous, kernel-timestamped
// prober (icmp_linux.go); see that file for why.
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

	// New defers resolution; SetNetwork pins the family so RunWithContext resolves
	// v4/v6 as configured. A resolve failure (no record in the pinned family, or an
	// IP literal of the other family) surfaces from RunWithContext below as Failed.
	pinger := probing.New(p.addr)
	if p.network != "" {
		pinger.SetNetwork(p.network)
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

	err := pinger.RunWithContext(ctx)
	if err != nil {
		return failedResult
	}

	st := pinger.Statistics()
	if st.PacketsRecv > 0 {
		ttl := -1
		if len(st.TTLs) > 0 {
			ttl = int(st.TTLs[0])
		}

		return success(float64(st.AvgRtt.Microseconds())/usPerMs, ttl)
	}

	return failedResult
}
