package prober

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const tcpTimeout = 5 * time.Second

// tcpPinger opens and closes one ordinary TCP connection per probe. No external
// command or raw socket is needed. Only an established connection confirms a response.
type tcpPinger struct {
	dest    probe.Destination
	port    string
	network string // LookupNetIP network, fixed by Compile.
	dial    func(context.Context, string, string) (net.Conn, error)
}

func newTCPPinger(dest probe.Destination, params probe.TCP) *tcpPinger {
	var dialer net.Dialer

	return &tcpPinger{
		dest:    dest,
		port:    params.Port.String(),
		network: resolveNetwork(params.Family),
		dial:    dialer.DialContext,
	}
}

func (p *tcpPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, tcpTimeout)
	defer cancel()

	addr, err := resolveProbeAddress(ctx, p.dest, p.port, p.network)
	if err != nil || ctx.Err() != nil {
		return probe.UnavailableResult()
	}

	start := time.Now()
	conn, err := p.dial(ctx, "tcp", addr)
	rtt := float64(time.Since(start).Microseconds()) / usPerMs

	if err == nil {
		discard(conn.Close())
	}

	return tcpDialResult(err, rtt)
}

// DNS failures are handled before dialing. Only a TCP timeout counts as loss;
// cancellation and local socket/routing failures did not observe the target.
// ECONNREFUSED alone cannot distinguish a TCP RST from ICMP port-unreachable or
// local rejection, so a refusal is unavailable rather than evidence of a response.
func tcpDialResult(err error, rtt float64) probe.Result {
	if err == nil {
		return probe.SuccessResult(rtt)
	}

	if errors.Is(err, context.Canceled) {
		return probe.UnavailableResult()
	}

	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTCPTimeout) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return probe.FailedResult()
	}

	return probe.UnavailableResult()
}
