//go:build manual

// These tests perform real ICMP and are excluded from the default suite. Run
// them explicitly to verify native ICMP works on the host (notably on Windows,
// where privileged mode is required but no admin elevation should be needed):
//
//	go test -tags manual -run TestICMP -v ./internal/infrastructure/prober

package prober

import (
	"sync"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestICMPLoopback(t *testing.T) {
	p, err := New(compiled(t, probe.Spec{Addr: "127.0.0.1"}), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	res := p.Send(t.Context())
	if !res.IsSuccess() {
		t.Fatalf("loopback ping failed: %+v", res)
	}

	t.Logf("loopback rtt=%.3fms", res.RTT)
}

// TestICMPConcurrent checks that several native ICMP probes really run in
// parallel (what -a relies on). Five unreachable TEST-NET addresses each hit the
// 1s timeout; concurrent => ~1s total, serialized => ~5s.
func TestICMPConcurrent(t *testing.T) {
	addrs := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"}
	start := time.Now()

	var wg sync.WaitGroup

	for _, a := range addrs {
		plan := compiled(t, probe.Spec{Addr: a})

		wg.Go(func() {
			p, err := New(plan, "row#1")
			if err != nil {
				return
			}

			p.Send(t.Context())
		})
	}

	wg.Wait()

	elapsed := time.Since(start)
	t.Logf("5 concurrent unreachable pings took %v", elapsed)

	if elapsed > 2500*time.Millisecond {
		t.Errorf("ICMP probes appear serialized: %v (expected ~1s if concurrent)", elapsed)
	}
}

// pause waits d, or returns early when the test is canceled.
func pause(t *testing.T, d time.Duration) {
	t.Helper()

	select {
	case <-t.Context().Done():
	case <-time.After(d):
	}
}
