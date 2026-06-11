//go:build manual

// These tests perform real QUIC handshakes over the network and are excluded from
// the default suite. Run them explicitly to verify the QUIC probe works on the host:
//
//	go test -tags manual -run TestQUIC -v ./internal/ping
package ping

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestQUICReachable handshakes against public HTTP/3 endpoints and expects at least
// one to succeed (a single endpoint being down must not fail the sanity check). It
// exercises both the hostname-SNI path and the bare-IP-with-explicit-sni path.
func TestQUICReachable(t *testing.T) {
	targets := []struct {
		name  string
		addr  string
		relay map[string]string
	}{
		{"dns.google", "dns.google", map[string]string{"via": "quic"}},
		{"cloudflare", "1.1.1.1", map[string]string{"via": "quic", "sni": "cloudflare-dns.com"}},
	}

	anyOK := false

	for _, tg := range targets {
		p, err := newQUICPinger(Spec{Addr: tg.addr, Relay: tg.relay})
		if err != nil {
			t.Fatalf("%s: newQUICPinger error: %v", tg.name, err)
		}

		res := p.Send(context.Background())
		if res.Success {
			anyOK = true

			t.Logf("%s: rtt=%.3fms", tg.name, res.RTT)
		} else {
			t.Logf(
				"%s: unreachable (code=%d) — may be a local network restriction",
				tg.name,
				res.Code,
			)
		}
	}

	if !anyOK {
		t.Error("no public QUIC endpoint reachable; check network/UDP 443 egress")
	}
}

// TestQUICUnreachable dials a TEST-NET-1 (RFC 5737) address that speaks no QUIC and
// expects a timeout failure bounded by quicTimeout — not a hang and not a success.
func TestQUICUnreachable(t *testing.T) {
	p, err := newQUICPinger(Spec{Addr: "192.0.2.1", Relay: map[string]string{"via": "quic"}})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res := p.Send(context.Background())
	elapsed := time.Since(start)

	if res.Success {
		t.Fatalf("unreachable TEST-NET address reported up: %+v", res)
	}

	t.Logf("unreachable failed as expected in %v (code=%d)", elapsed, res.Code)

	if elapsed > quicTimeout+2*time.Second {
		t.Errorf("probe overran its timeout: %v (quicTimeout=%v)", elapsed, quicTimeout)
	}
}

// TestQUICNoLeak guards the design's central risk: Send dials a fresh quic.Transport
// and UDP socket on every probe, and deadman runs it on a loop forever. It verifies
// that CloseWithError reclaims the per-probe goroutines (and, on Linux, file
// descriptors) so continuous operation does not leak. The success path is the hot
// path, so the loop runs against a real endpoint.
func TestQUICNoLeak(t *testing.T) {
	p, err := newQUICPinger(
		Spec{Addr: "1.1.1.1", Relay: map[string]string{"via": "quic", "sni": "cloudflare-dns.com"}},
	)
	if err != nil {
		t.Fatal(err)
	}

	// The first successful dial initializes one-time runtime state, so the baseline is
	// taken after it rather than before.
	if res := p.Send(context.Background()); !res.Success {
		t.Skip("endpoint unreachable; cannot run the success-path leak check")
	}

	stable := func() (int, int) {
		runtime.GC()
		time.Sleep(100 * time.Millisecond)

		return runtime.NumGoroutine(), openFDs()
	}

	baseG, baseFD := stable()

	const iterations = 30

	for i := range iterations {
		if res := p.Send(context.Background()); !res.Success {
			t.Skipf("endpoint became unreachable at iter %d; leak check inconclusive", i)
		}
	}

	afterG, afterFD := stable()

	t.Logf(
		"goroutines %d -> %d; fds %d -> %d over %d probes",
		baseG,
		afterG,
		baseFD,
		afterFD,
		iterations,
	)

	// A per-probe leak would grow roughly linearly with iterations; allow small slack
	// for runtime/GC scheduling jitter.
	if afterG-baseG > 3 {
		t.Errorf("goroutine leak: grew by %d over %d probes", afterG-baseG, iterations)
	}

	if baseFD > 0 && afterFD-baseFD > 3 {
		t.Errorf("fd leak: grew by %d over %d probes", afterFD-baseFD, iterations)
	}
}

// openFDs returns the number of open file descriptors on Linux (via /proc/self/fd), or
// 0 elsewhere — the caller then skips the fd assertion.
func openFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}

	return len(entries)
}
