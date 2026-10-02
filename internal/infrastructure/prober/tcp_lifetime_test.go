package prober

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func tcpWithParams(t *testing.T, address string, params probe.TCP) *tcpPinger {
	t.Helper()

	p, err := New(compiled(t, probe.Spec{Addr: address, Params: params}), "tcp")
	if err != nil {
		t.Fatal(err)
	}

	tcp, ok := p.(*tcpPinger)
	if !ok {
		t.Fatalf("unexpected pinger %T", p)
	}

	return tcp
}

func tcpForHostname(t *testing.T, listener net.Listener, family probe.Family) *tcpPinger {
	t.Helper()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	return tcpWithParams(
		t,
		"monitor.example.invalid.",
		probe.TCP{Port: probe.PortNumber(number), Family: family},
	)
}

func TestTCPHostnameConnectsInCompiledFamily(t *testing.T) {
	for _, c := range []struct {
		family probe.Family
		host   string
	}{
		{probe.FamilyUnknown, "127.0.0.1"},
		{probe.FamilyIPv4, "127.0.0.1"},
		{probe.FamilyIPv6, "::1"},
	} {
		t.Run(c.family.String(), func(t *testing.T) {
			listener := listenTCP(t, c.host)
			useProbeDNS(
				t,
				[]netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")},
				nil,
			)
			p := tcpForHostname(t, listener, c.family)

			if result := p.Send(t.Context()); result.Code != probe.Success {
				t.Fatalf("hostname connection = %+v", result)
			}

			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}

			discard(conn.Close())
		})
	}
}

func TestTCPDNSFailureDoesNotDialOrInventLoss(t *testing.T) {
	useProbeDNS(t, nil, nil)
	p := tcpWithParams(t, "monitor.example.invalid.", probe.TCP{Port: probe.PortNumber(53)})
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("dial attempted after a DNS failure")

		return nil, context.DeadlineExceeded
	}

	assertUnobservedProbe(t, p.Send(t.Context()), probe.Unavailable)
}

func TestTCPDNSLatencyIsOutsideRTT(t *testing.T) {
	listener := listenTCP(t, "127.0.0.1")

	const lookupDelay = 500 * time.Millisecond

	useProbeDNS(t, []netip.Addr{netip.MustParseAddr("127.0.0.1")}, func(ctx context.Context) error {
		timer := time.NewTimer(lookupDelay)
		defer timer.Stop()

		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	p := tcpForHostname(t, listener, probe.FamilyIPv4)

	start := time.Now()
	result := p.Send(t.Context())
	elapsed := time.Since(start)

	if result.Code != probe.Success {
		t.Fatalf("hostname connection = %+v", result)
	}

	// Leave half the injected DNS delay as scheduling slack, rather than requiring
	// an absolute loopback RTT on a potentially busy test runner.
	rtt := time.Duration(result.RTT * float64(time.Millisecond))
	if elapsed < lookupDelay || rtt >= elapsed-lookupDelay/2 {
		t.Fatalf("DNS entered RTT: elapsed=%v, RTT=%v, DNS delay=%v", elapsed, rtt, lookupDelay)
	}
}

func TestTCPDialHonorsCallerLifetime(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}

		t.Run(name, func(t *testing.T) {
			p := tcpWithParams(t, "127.0.0.1", probe.TCP{Port: probe.PortNumber(53)})
			started, release := make(chan struct{}), make(chan struct{})

			t.Cleanup(func() { close(release) })

			p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" || address != "127.0.0.1:53" {
					t.Errorf("dial = %s/%s", network, address)
				}

				close(started)

				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return nil, context.Canceled
				}
			}

			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
			}
			defer cancel()

			results := make(chan probe.Result, 1)
			go func() { results <- p.Send(ctx) }()

			awaitProbeSignal(t, started)

			if !deadline {
				cancel()
				assertUnobservedProbe(t, awaitProbeResult(ctx, t, results), probe.Unavailable)

				return
			}

			assertTCPTimeout(t, awaitProbeResult(ctx, t, results))
		})
	}
}

func TestTCPDialReceivesProbeDeadline(t *testing.T) {
	p := tcpWithParams(t, "127.0.0.1", probe.TCP{Port: probe.PortNumber(53)})
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > tcpTimeout {
			t.Errorf("dial deadline = %v, present=%v; want at most %v", deadline, ok, tcpTimeout)
		}

		return nil, context.DeadlineExceeded
	}

	assertTCPTimeout(t, p.Send(t.Context()))
}

func assertTCPTimeout(t *testing.T, result probe.Result) {
	t.Helper()

	if result.Code != probe.Failed || result.RTT != 0 {
		t.Fatalf("unanswered connection = %+v, want loss without RTT", result)
	}

	target := monitor.NewTarget("tcp", "target", "127.0.0.1")
	target.Consume(result)

	if got := target.Snapshot(); got.State != monitor.Down || got.Snt != 1 || got.Loss != 1 {
		t.Fatalf("TCP timeout was not counted as loss: %+v", got)
	}
}
