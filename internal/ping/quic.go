package ping

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
)

const quicTimeout = 5 * time.Second

const (
	defaultQUICPort = "443"
	defaultQUICALPN = "h3"
)

// quicPinger probes by performing a fresh QUIC (TLS 1.3) handshake on each Send and
// measuring the dial->handshake-complete wall-clock as the RTT. It is the in-process
// sibling of the tcp= connect-probe: it targets an endpoint known to speak QUIC on a
// port, not an arbitrary IP. The tls.Config is built once in the constructor (the
// routeros pattern). Unlike routeros, verify defaults OFF (insecure), because QUIC
// probes commonly target endpoints by IP, where a SAN/cert check would fail every probe.
//
// The RTT is the handshake-completion time, so it includes the TLS 1.3 crypto work on
// both ends and reads slightly higher than an ICMP or TCP-connect probe. No TTL is
// available from a QUIC handshake, so Result.TTL is always -1.
type quicPinger struct {
	addr      string // host:port, ready for quic.DialAddr.
	tlsConfig *tls.Config
}

func newQUICPinger(s Spec) (Pinger, error) {
	alpn := s.Relay["alpn"]
	if alpn == "" {
		alpn = defaultQUICALPN
	}

	// verify defaults OFF for QUIC (InsecureSkipVerify=true) — the opposite of the
	// routeros default. Only an explicit truthy value turns verification on; an absent
	// or unrecognized value stays insecure.
	insecure := true

	switch strings.ToLower(s.Relay["verify"]) {
	case "on", "true", "yes", "1":
		insecure = false
	default:
		// "", off/false/no/0, or any unrecognized value: stay insecure.
	}

	// SNI: an explicit sni= wins; otherwise the hostname Addr; otherwise "" for a bare
	// IP literal. The SNI field is sent even with InsecureSkipVerify, and real h3
	// front-ends (Cloudflare/Google) require it — so a bare-IP target may need an
	// explicit sni= to complete the handshake (documented in the README).
	sni := s.Relay["sni"]
	if sni == "" && net.ParseIP(s.Addr) == nil {
		sni = s.Addr
	}

	// #nosec G402 -- TLS verification is opt-in via verify=on; QUIC probes target
	// endpoints (often by IP) where a SAN/cert check would otherwise fail every probe.
	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecure,
		NextProtos:         []string{alpn},
		ServerName:         sni,
	}

	return &quicPinger{
		addr:      net.JoinHostPort(s.Addr, quicPort(s)),
		tlsConfig: tlsConfig,
	}, nil
}

func (p *quicPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, quicTimeout)
	defer cancel()

	// DialAddr blocks until the 1-RTT handshake completes; ctx cancellation aborts the
	// dial. A nil quic.Config leaves the context as the single timeout authority.
	start := time.Now()

	conn, err := quic.DialAddr(ctx, p.addr, p.tlsConfig, nil)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	// Capture the RTT before closing so the close path never contaminates it.
	rtt := float64(time.Since(start).Microseconds()) / usPerMs

	// Each DialAddr spins up an internal Transport and ephemeral UDP socket; close it
	// or leak a socket and goroutines on every probe.
	_ = conn.CloseWithError(0, "")

	return Result{Success: true, Code: Success, RTT: rtt, TTL: -1}
}

// quicPort returns the dial port for a QUIC target: an explicit port= or the 443 default.
func quicPort(s Spec) string {
	if p := s.Relay["port"]; p != "" {
		return p
	}

	return defaultQUICPort
}

// quicLabel renders the VIA-column label for a QUIC target: "quic <port>".
func quicLabel(s Spec) string {
	return viaQUIC + " " + quicPort(s)
}
