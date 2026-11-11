//go:build manual

// This test sends a real ICMP echo to 192.0.2.1 (TEST-NET-1, which does not answer), so it
// is excluded from the default suite:
//
//	go test -tags manual -run TestDirectReplyWaitStartsAfterLookup -v ./internal/infrastructure/prober

package prober

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// A slow name lookup must not shorten the reply wait: the echo still waits its full
// icmpTimeout after the lookup, rather than whatever the lookup left of one shared second.
// Both ICMP implementations run here.
func TestDirectReplyWaitStartsAfterLookup(t *testing.T) {
	if !directICMPAvailable() {
		t.Skip("no ICMP socket can be opened on this host")
	}

	const lookup = 800 * time.Millisecond

	for _, adapter := range []string{"native_icmp", "portable_icmp"} {
		t.Run(adapter, func(t *testing.T) {
			useProbeDNS(
				t,
				[]netip.Addr{netip.MustParseAddr("192.0.2.1")},
				func(ctx context.Context) error {
					select {
					case <-time.After(lookup):
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
			)

			spec := probe.Spec{
				Addr:   "slow-lookup.example.invalid.",
				Params: probe.Direct{Family: probe.FamilyIPv4},
			}

			p, err := New(compiled(t, spec), "row#1")
			if err != nil {
				t.Fatal(err)
			}

			send := p.Send
			if adapter == "portable_icmp" {
				icmp, ok := p.(*icmpPinger)
				if !ok {
					t.Fatalf("unexpected adapter %T", p)
				}

				send = icmp.sendPortable
			}

			start := time.Now()
			result := send(t.Context())
			elapsed := time.Since(start)

			if result.Code == probe.Unavailable {
				t.Skipf("the echo could not be sent here: %+v", result)
			}

			if result.Code != probe.Failed || elapsed < lookup+icmpTimeout-100*time.Millisecond {
				t.Fatalf("result %+v after %v; want a loss after the lookup plus a full %v wait",
					result, elapsed, icmpTimeout)
			}
		})
	}
}
