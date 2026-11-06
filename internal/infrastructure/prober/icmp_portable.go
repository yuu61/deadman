package prober

import (
	"context"
	"errors"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// sendPortable sends a native ICMP echo. On BSD an echo too big for a raw socket's default
// space uses a socket with payload-sized buffers; every other echo uses pro-bing, or
// sendScoped for a zoned source. Linux replaces this with a synchronous, kernel-timestamped
// prober (icmp_linux.go); see that file for why.
func (p *icmpPinger) sendPortable(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, icmpTimeout)
	defer cancel()

	// Resolve under the caller lifetime before pro-bing starts its synchronous setup.
	dest, err := p.resolvePortable(ctx)
	if err != nil {
		return probe.UnavailableResult()
	}

	if needsNativeEcho(p.echo, nativeBSDEcho) {
		return p.sendNativeEcho(ctx, dest)
	}

	if source, ok := p.source.Addr(); ok && source.Zone() != "" {
		return p.sendScoped(ctx, dest, source)
	}

	pinger := probing.New(p.dest.String())
	pinger.SetIPAddr(dest)

	if p.network != "" {
		pinger.SetNetwork(p.network)
	}

	pinger.SetPrivileged(p.privileged)
	pinger.Count = 1
	pinger.Timeout = icmpTimeout
	pinger.Size = p.echo.Size.Bytes()

	// pro-bing's Source binds by address, InterfaceName by name.
	if addr, hasAddr := p.source.Addr(); hasAddr {
		pinger.Source = addr.String()
	} else if name, hasName := p.source.Interface(); hasName {
		pinger.InterfaceName = name
	}

	err = pinger.RunWithContext(ctx)
	st := pinger.Statistics()

	return portableICMPResult(err, st)
}

// portableICMPResult distinguishes an echo that got no answer from a probe that
// never sent one. pro-bing returns context.DeadlineExceeded when our outer timer
// fires first, even when its own response timer would soon report normal loss.
func portableICMPResult(err error, st *probing.Statistics) probe.Result {
	if st.PacketsRecv > 0 {
		return probe.SuccessResult(float64(st.AvgRtt.Microseconds()) / usPerMs)
	}

	// pro-bing starts its own response timer only after DNS and socket setup. The
	// outer deadline may therefore end first, even after an echo was sent. A sent
	// echo with no reply is observed loss; a lookup/socket failure sent nothing.
	if st.PacketsSent > 0 && (err == nil || errors.Is(err, context.DeadlineExceeded)) {
		return probe.FailedResult()
	}

	return probe.UnavailableResult()
}
