//go:build manual

// These tests perform real ICMP and are excluded from the default suite. Run
// them explicitly to verify native ICMP works on the host (notably on Windows,
// where privileged mode is required but no admin elevation should be needed):
//
//	go test -tags manual -run TestICMP -v ./internal/ping
package ping

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestICMPLoopback(t *testing.T) {
	p, err := newICMPPinger(Spec{Addr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Send(context.Background())
	if !res.Success {
		t.Fatalf("loopback ping failed: %+v", res)
	}
	t.Logf("loopback rtt=%.3fms ttl=%d", res.RTT, res.TTL)
}

// TestICMPConcurrent checks that several native ICMP probes really run in
// parallel (what -a relies on). Five unreachable TEST-NET addresses each hit the
// 1s timeout; concurrent => ~1s total, serialized => ~5s.
func TestICMPConcurrent(t *testing.T) {
	addrs := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"}
	start := time.Now()
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			p, err := newICMPPinger(Spec{Addr: addr})
			if err != nil {
				return
			}
			p.Send(context.Background())
		}(a)
	}
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("5 concurrent unreachable pings took %v", elapsed)
	if elapsed > 2500*time.Millisecond {
		t.Errorf("ICMP probes appear serialized: %v (expected ~1s if concurrent)", elapsed)
	}
}
