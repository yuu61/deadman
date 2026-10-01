package prober

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const quicTimeout = 5 * time.Second

// quicPinger probes by performing a fresh QUIC (TLS 1.3) handshake on each Send and
// measuring the dial->handshake-complete wall-clock as the RTT. It is the in-process
// sibling of the TCP SYN probe: it targets an endpoint known to speak QUIC on a
// port, not an arbitrary IP. The tls.Config is built once in the constructor (the
// routeros pattern) from the plan, which fills in the port, ALPN, SNI and verification
// (off by default, because QUIC probes commonly target endpoints by IP).
//
// The RTT is the handshake-completion time, so it includes the TLS 1.3 crypto work on
// both ends and reads slightly higher than an ICMP or TCP-connect probe.
type quicPinger struct {
	dest      probe.Destination // a name is resolved per Send.
	port      string            // dial port, validated at construction.
	network   string            // LookupNetIP network: "ip4"/"ip6" pins resolve_family, "ip" = either.
	tlsConfig *tls.Config
}

func newQUICPinger(dest probe.Destination, params probe.QUIC) (probe.Pinger, error) {
	// The SNI field is sent even with verification off, and real h3 front ends
	// (Cloudflare/Google) require it — so a bare-IP target, whose plan has no SNI, may
	// need an explicit sni= to complete the handshake (documented in docs/configuration.md).
	//
	// #nosec G402 -- TLS verification is opt-in via verify=on; QUIC probes target
	// endpoints (often by IP) where a SAN/cert check would otherwise fail every probe.
	tlsConfig := &tls.Config{
		InsecureSkipVerify: params.Verify != probe.VerifyEnabled,
		NextProtos:         []string{params.ALPN},
		ServerName:         params.SNI,
	}

	// resolve_family pins a dual-stack hostname to A (ip4) or AAAA (ip6) records, like the
	// direct-ICMP path; unset lets the resolver choose. It is applied only to hostname
	// resolution — an IP literal already fixes its family.
	network := resolveNetwork(params.Family)
	if network == "" {
		network = networkAny
	}

	return &quicPinger{
		dest:      dest,
		port:      params.Port.String(),
		network:   network,
		tlsConfig: tlsConfig,
	}, nil
}

func (p *quicPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, quicTimeout)
	defer cancel()

	// Resolve the host OUTSIDE the RTT window so DNS latency/jitter does not inflate the
	// handshake measurement (matching the direct-ICMP path). The lookup still honors the
	// ctx timeout, so a hung resolver cannot overrun the probe.
	addr, err := p.resolveAddr(ctx)
	if err != nil {
		return probe.UnavailableResult()
	}

	// DialAddr blocks until the 1-RTT handshake completes; ctx cancellation aborts the
	// dial. A nil quic.Config leaves the context as the single timeout authority.
	start := time.Now()

	conn, err := quic.DialAddr(ctx, addr, p.tlsConfig, nil)
	if err != nil {
		return quicFailure(err)
	}

	// Capture the RTT before closing so the close path never contaminates it.
	rtt := float64(time.Since(start).Microseconds()) / usPerMs

	// Each DialAddr spins up an internal Transport and ephemeral UDP socket; close it
	// or leak a socket and goroutines on every probe.
	_ = conn.CloseWithError(0, "")

	return probe.SuccessResult(rtt)
}

// resolveAddr returns the host:port to dial with the host already resolved to an IP, so
// quic.DialAddr performs no DNS inside the RTT window. An address destination (a zoned
// IPv6 one included) is dialed as it is; a name is resolved here under ctx. Like the rest
// of deadman, it probes the resolver's first address rather than racing several.
func (p *quicPinger) resolveAddr(ctx context.Context) (string, error) {
	if a, ok := p.dest.IP(); ok {
		return net.JoinHostPort(a.String(), p.port), nil
	}

	name, _ := p.dest.Name()

	ips, err := net.DefaultResolver.LookupNetIP(ctx, p.network, name)
	if err != nil {
		return "", err
	}

	if len(ips) == 0 {
		return "", fmt.Errorf("quic: no addresses for %s", name)
	}

	return net.JoinHostPort(ips[0].String(), p.port), nil
}

// quicFailure treats the handshake or dial deadline as an observed lack of response.
// Socket, TLS, protocol and local setup errors do not establish target loss.
func quicFailure(err error) probe.Result {
	var timeout *quic.HandshakeTimeoutError
	if errors.As(err, &timeout) || errors.Is(err, context.DeadlineExceeded) {
		return probe.FailedResult()
	}

	return probe.UnavailableResult()
}
