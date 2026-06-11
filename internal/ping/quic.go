package ping

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"strconv"
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
	host      string // hostname or IP literal, resolved per Send (no port).
	port      string // dial port, validated at construction.
	network   string // LookupNetIP network: "ip4"/"ip6" pins resolve_family, "ip" = either.
	tlsConfig *tls.Config
}

func newQUICPinger(s Spec) (Pinger, error) {
	const (
		minPort = 1
		maxPort = 65535
	)

	// Validate port= at construction so a typo (port=abc / 0 / 99999) surfaces as a
	// startup warning and a permanent-failure row (model.go), like routeros rejecting a
	// missing username — rather than a silent X the operator must debug by staring at it.
	port := quicPort(s)

	n, err := strconv.Atoi(port)
	if err != nil || n < minPort || n > maxPort {
		return nil, fmt.Errorf(
			"quic: invalid port %q for %s (want %d-%d)",
			port,
			s.Addr,
			minPort,
			maxPort,
		)
	}

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

	// SNI: an explicit sni= wins; otherwise the hostname Addr; otherwise "" for an IP
	// literal. isIPLiteral uses netip.ParseAddr (not net.ParseIP) so a zoned IPv6 literal
	// such as fe80::1%eth0 is recognized as a literal and its zone never leaks into the
	// SNI. The SNI field is sent even with InsecureSkipVerify, and real h3 front-ends
	// (Cloudflare/Google) require it — so a bare-IP target may need an explicit sni= to
	// complete the handshake (documented in the README).
	sni := s.Relay["sni"]
	if sni == "" && !isIPLiteral(s.Addr) {
		sni = s.Addr
	}

	// #nosec G402 -- TLS verification is opt-in via verify=on; QUIC probes target
	// endpoints (often by IP) where a SAN/cert check would otherwise fail every probe.
	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecure,
		NextProtos:         []string{alpn},
		ServerName:         sni,
	}

	// resolve_family pins a dual-stack hostname to A (ip4) or AAAA (ip6) records, like the
	// direct-ICMP path; an unset/unrecognized value lets the resolver choose. It is
	// applied only to hostname resolution — an IP literal already fixes its family.
	network := resolveNetwork(s)
	if network == "" {
		network = networkAny
	}

	return &quicPinger{host: s.Addr, port: port, network: network, tlsConfig: tlsConfig}, nil
}

func (p *quicPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, quicTimeout)
	defer cancel()

	// Resolve the host OUTSIDE the RTT window so DNS latency/jitter does not inflate the
	// handshake measurement (matching the direct-ICMP path). The lookup still honors the
	// ctx timeout, so a hung resolver cannot overrun the probe.
	addr, err := p.resolveAddr(ctx)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	// DialAddr blocks until the 1-RTT handshake completes; ctx cancellation aborts the
	// dial. A nil quic.Config leaves the context as the single timeout authority.
	start := time.Now()

	conn, err := quic.DialAddr(ctx, addr, p.tlsConfig, nil)
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

// resolveAddr returns the host:port to dial with the host already resolved to an IP, so
// quic.DialAddr performs no DNS inside the RTT window. An IP-literal host (including a
// zoned IPv6 literal) is returned unchanged; a hostname is resolved here under ctx. Like
// the rest of deadman, it probes the resolver's first address rather than racing several.
func (p *quicPinger) resolveAddr(ctx context.Context) (string, error) {
	if isIPLiteral(p.host) {
		return net.JoinHostPort(p.host, p.port), nil
	}

	ips, err := net.DefaultResolver.LookupNetIP(ctx, p.network, p.host)
	if err != nil {
		return "", err
	}

	if len(ips) == 0 {
		return "", fmt.Errorf("quic: no addresses for %s", p.host)
	}

	return net.JoinHostPort(ips[0].String(), p.port), nil
}

// isIPLiteral reports whether host is an IP-address literal (v4, v6, or a zoned v6 such
// as fe80::1%eth0). It uses netip.ParseAddr rather than net.ParseIP precisely because
// net.ParseIP rejects the zone form, which would misclassify a zoned literal as a
// hostname (icmp_linux.go uses netip.ParseAddr for the same reason).
func isIPLiteral(host string) bool {
	_, err := netip.ParseAddr(host)

	return err == nil
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
