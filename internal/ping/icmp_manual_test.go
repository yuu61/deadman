//go:build manual

// These tests perform real ICMP and are excluded from the default suite. Run
// them explicitly to verify native ICMP works on the host (notably on Windows,
// where privileged mode is required but no admin elevation should be needed):
//
//	go test -tags manual -run TestICMP -v ./internal/ping
package ping

import (
	"context"
	"testing"
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
