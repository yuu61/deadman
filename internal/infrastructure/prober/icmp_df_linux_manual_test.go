//go:build linux && manual

package prober

import (
	"net/netip"
	"os"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Run in an isolated routed network with a 9000-byte sender link and a 1500-byte
// router egress. Set DEADMAN_TEST_PMTU_TARGET to the first of two adjacent target
// addresses (both must answer echo). Separate targets keep each socket mode's first
// attempt free of a PMTU cache entry. Raw mode requires CAP_NET_RAW.
func TestICMPPathMTU(t *testing.T) {
	target := os.Getenv("DEADMAN_TEST_PMTU_TARGET")
	if target == "" {
		t.Skip("set DEADMAN_TEST_PMTU_TARGET in an isolated routed test network")
	}

	address, err := netip.ParseAddr(target)
	if err != nil {
		t.Fatal(err)
	}

	for _, privileged := range []bool{false, true} {
		name := "datagram"
		if privileged {
			name = "raw"
			address = address.Next()
		}

		t.Run(name, func(t *testing.T) {
			params := probe.Direct{Echo: probe.EchoOptions{Size: probe.PayloadBytes(24), DF: true}}
			plan := compiled(t, probe.Spec{Addr: address.String(), Params: params})

			pinger, err := New(plan, "pmtu#1")
			if err != nil {
				t.Fatal(err)
			}

			p, ok := pinger.(*icmpPinger)
			if !ok {
				t.Fatalf("unexpected pinger type %T", pinger)
			}

			p.privileged = privileged
			if result := p.Send(t.Context()); !result.IsSuccess() {
				t.Fatalf("small control echo: %+v", result)
			}

			p.echo.Size = probe.PayloadBytes(2000)
			for attempt := range 2 {
				if result := p.Send(t.Context()); result.Code != probe.Unavailable {
					t.Fatalf("attempt %d: MTU error = %+v, want Unavailable", attempt+1, result)
				}
			}
		})
	}
}
