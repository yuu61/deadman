package prober

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func awaitProbeSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not reach the expected stage")
	}
}

func awaitProbeResult(ctx context.Context, t *testing.T, results <-chan probe.Result) probe.Result {
	t.Helper()

	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("caller context did not stop")
	}

	// This is shorter than the ICMP adapter's own one-second timeout: losing the
	// caller context must fail even if the adapter eventually returns on its timer.
	select {
	case result := <-results:
		return result
	case <-time.After(500 * time.Millisecond):
		t.Fatal("probe did not finish after its caller stopped")

		return probe.Result{}
	}
}

func assertUnobservedProbe(t *testing.T, result probe.Result, want probe.ResultCode) {
	t.Helper()

	if result.Code != want {
		t.Fatalf("result = %+v, want code %v", result, want)
	}

	target := monitor.NewTarget("row#1", "target", "192.0.2.1")
	target.Consume(result)

	if got := target.Snapshot(); got.State != monitor.Unknown || got.Snt != 0 || got.Loss != 0 {
		t.Fatalf("unobserved probe changed target statistics: %+v", got)
	}
}

// Cancel only after resolution starts, so this tests an in-flight lookup rather than
// the early return for an already canceled context. Both ICMP implementations run here.
func TestProbeDNSHonorsCallerLifetime(t *testing.T) {
	for _, adapter := range []string{"native_icmp", "portable_icmp", "quic", "tcp"} {
		for _, deadline := range []bool{false, true} {
			name := "cancel"
			if deadline {
				name = "deadline"
			}

			t.Run(adapter+"/"+name, func(t *testing.T) {
				started, release := make(chan struct{}, 1), make(chan struct{})
				original := net.DefaultResolver
				net.DefaultResolver = &net.Resolver{
					PreferGo: true,
					Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
						select {
						case started <- struct{}{}:
						default:
						}

						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case <-release:
							return nil, context.Canceled
						}
					},
				}

				t.Cleanup(func() {
					close(release)

					net.DefaultResolver = original
				})

				spec := probe.Spec{Addr: "caller-lifetime.example.invalid"}
				if adapter == "quic" {
					spec.Params = probe.QUIC{}
				}

				if adapter == "tcp" {
					spec.Params = probe.TCP{Port: probe.PortNumber(53)}
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

				ctx, cancel := context.WithCancel(t.Context())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
				}
				defer cancel()

				results := make(chan probe.Result, 1)
				go func() { results <- send(ctx) }()

				awaitProbeSignal(t, started)

				if !deadline {
					cancel()
				}

				assertUnobservedProbe(t, awaitProbeResult(ctx, t, results), probe.Unavailable)
			})
		}
	}
}
