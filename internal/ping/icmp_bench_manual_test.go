//go:build manual && linux

// Manual benchmark for the Linux synchronous + SO_TIMESTAMPNS prober. It is excluded
// from the default suite (it sends real ICMP). Run it to confirm the loopback RTT sits
// near native ping (sub-50µs avg) rather than carrying goroutine-wakeup overhead:
//
//	go test -tags manual -run TestICMPRTTLoopback -v ./internal/ping
//
// A kernel-timestamped recv lands avg ~0.02-0.04ms; a userspace fallback (no
// SO_TIMESTAMPNS) sits closer to ~0.05ms. Run as root too to exercise the raw path.
package ping

import (
	"context"
	"sort"
	"testing"
	"time"
)

func benchLoopback(t *testing.T, addr string) {
	t.Helper()

	p, err := newICMPPinger(Spec{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}

	const n = 50

	var samples []float64

	for i := 0; i < n; i++ {
		res := p.Send(context.Background())
		if !res.Success {
			continue
		}

		samples = append(samples, res.RTT)
		time.Sleep(10 * time.Millisecond)
	}

	if len(samples) == 0 {
		t.Fatalf("%s: no successful probes", addr)
	}

	sort.Float64s(samples)

	var tot float64
	for _, s := range samples {
		tot += s
	}

	pct := func(q float64) float64 { return samples[int(q*float64(len(samples)-1))] }
	privileged := useICMPPrivileged()
	t.Logf("%s [raw=%v] n=%d min=%.4f avg=%.4f p50=%.4f p90=%.4f p99=%.4f max=%.4f ms",
		addr, privileged, len(samples), samples[0], tot/float64(len(samples)),
		pct(0.5), pct(0.9), pct(0.99), samples[len(samples)-1])
}

func TestICMPRTTLoopback(t *testing.T) {
	t.Run("v4", func(t *testing.T) { benchLoopback(t, "127.0.0.1") })
	t.Run("v6", func(t *testing.T) { benchLoopback(t, "::1") })
}

// TestICMPSourceEgress exercises source= on the live socket: an interface name (the
// IP_PKTINFO egress path that replaced SO_BINDTODEVICE) and a source IP (the bind path).
// Both must succeed to 127.0.0.1 over loopback on the unprivileged datagram socket.
//
//	go test -tags manual -run TestICMPSourceEgress -v ./internal/ping
func TestICMPSourceEgress(t *testing.T) {
	cases := map[string]string{"interface=lo": "lo", "ip=127.0.0.1": "127.0.0.1"}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := newICMPPinger(Spec{Addr: "127.0.0.1", Source: source})
			if err != nil {
				t.Fatal(err)
			}

			res := p.Send(context.Background())
			if !res.Success {
				t.Fatalf("source=%q probe failed: %+v", source, res)
			}

			t.Logf("source=%q rtt=%.4fms ttl=%d", source, res.RTT, res.TTL)
		})
	}
}

// TestICMPContextCancel verifies a ctx cancellation interrupts the recv wait: a probe to
// an unreachable TEST-NET address must return shortly after cancel, not at the ~1s timeout.
//
//	go test -tags manual -run TestICMPContextCancel -v ./internal/ping
func TestICMPContextCancel(t *testing.T) {
	p, err := newICMPPinger(Spec{Addr: "192.0.2.1"}) // TEST-NET-1: never replies.
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	res := p.Send(ctx)
	elapsed := time.Since(start)

	t.Logf("cancelled probe returned after %v (success=%v)", elapsed, res.Success)

	if res.Success {
		t.Fatal("probe to TEST-NET-1 must not succeed")
	}

	if elapsed > 500*time.Millisecond {
		t.Fatalf("ctx cancel was not honored: returned after %v, want ~100ms", elapsed)
	}
}
