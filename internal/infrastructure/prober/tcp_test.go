package prober

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestTCPDialResults(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want probe.ResultCode
	}{
		{"connected", nil, probe.Success},
		// RST, ICMP port-unreachable and local rejection can share this errno.
		{"ambiguous_refusal", os.NewSyscallError("connect", errTCPRefused), probe.Unavailable},
		{"socket_timeout", os.NewSyscallError("connect", errTCPTimeout), probe.Failed},
		{"deadline", context.DeadlineExceeded, probe.Failed},
		{"cancel", context.Canceled, probe.Unavailable},
		{"no_route", errors.New("network unreachable"), probe.Unavailable},
		{"socket_error", errors.New("too many open files"), probe.Unavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.err
			if err != nil {
				err = &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("dial failed: %w", err)}
			}

			result := tcpDialResult(err, 1.25)
			if result.Code != c.want {
				t.Fatalf("result = %+v, want %v", result, c.want)
			}

			if c.want == probe.Success && result.RTT != 1.25 {
				t.Errorf("RTT = %v, want 1.25", result.RTT)
			}

			if c.want != probe.Success && result.RTT != 0 {
				t.Errorf("unobserved RTT = %v, want 0", result.RTT)
			}

			target := monitor.NewTarget("tcp", "host", "192.0.2.1")
			target.Consume(result)

			if c.want == probe.Unavailable && target.Snapshot().Stats != (monitor.Stats{}) {
				t.Fatal("unobserved result entered statistics")
			}

			if c.want == probe.Failed && target.Snapshot().Loss != 1 {
				t.Fatal("unanswered connection was not counted as loss")
			}
		})
	}
}

// Every successful round must close its connection without sending application data.
func TestTCPConnectAndClose(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			listener := listenTCP(t, host)
			p := tcpForEndpoint(t, listener.Addr().String())

			const rounds = 12

			closed := make(chan error, 1)

			go func() {
				for range rounds {
					conn, err := listener.Accept()
					if err != nil {
						closed <- err

						return
					}

					err = conn.SetReadDeadline(time.Now().Add(time.Second))
					if err == nil {
						var data [1]byte

						count, readErr := conn.Read(data[:])
						if count != 0 || !errors.Is(readErr, io.EOF) {
							err = fmt.Errorf(
								"connection did not close without data: count=%d, err=%w",
								count,
								readErr,
							)
						}
					}

					discard(conn.Close())

					if err != nil {
						closed <- err

						return
					}
				}

				closed <- nil
			}()

			for range rounds {
				if result := p.Send(t.Context()); result.Code != probe.Success {
					t.Fatalf("connect result = %+v", result)
				}
			}

			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("server did not observe all connections closing")
			}
		})
	}
}

// Even a real loopback RST is ambiguous through the ordinary dial API.
func TestTCPConnectionRefusedIsUnobserved(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			listener := listenTCP(t, host)
			p := tcpForEndpoint(t, listener.Addr().String())

			err := listener.Close()
			if err != nil {
				t.Fatal(err)
			}

			assertUnobservedProbe(t, p.Send(t.Context()), probe.Unavailable)
		})
	}
}

func listenTCP(t *testing.T, host string) net.Listener {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		if host == "::1" {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}

		t.Fatal(err)
	}

	t.Cleanup(func() { discard(listener.Close()) })

	return listener
}

func tcpForEndpoint(t *testing.T, endpoint string) probe.Pinger {
	t.Helper()

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	p, err := New(
		compiled(t, probe.Spec{Addr: host, Params: probe.TCP{Port: probe.PortNumber(number)}}),
		"tcp",
	)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestTCPCanceledBeforeSendDoesNotInventLoss(t *testing.T) {
	p, err := New(
		compiled(t, probe.Spec{Addr: "127.0.0.1", Params: probe.TCP{Port: probe.PortNumber(53)}}),
		"tcp",
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertUnobservedProbe(t, p.Send(ctx), probe.Unavailable)

	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	assertUnobservedProbe(t, p.Send(ctx), probe.Unavailable)
}

func TestNewTCPPingerFamily(t *testing.T) {
	for _, c := range []struct {
		addr    string
		family  probe.Family
		network string
	}{
		{"example.com", probe.FamilyUnknown, networkIPv4},
		{"example.com", probe.FamilyIPv4, networkIPv4},
		{"example.com", probe.FamilyIPv6, networkIPv6},
		{"::1", probe.FamilyUnknown, networkIPv6},
	} {
		plan := compiled(
			t,
			probe.Spec{
				Addr:   c.addr,
				Params: probe.TCP{Port: probe.PortNumber(53), Family: c.family},
			},
		)

		p, err := New(plan, "tcp")
		if err != nil {
			t.Fatal(err)
		}

		tcp, ok := p.(*tcpPinger)
		if !ok || tcp.network != c.network || tcp.dest != plan.Destination() || tcp.port != "53" {
			t.Errorf("%s/%s: unexpected pinger %+v", c.addr, c.family, p)
		}
	}
}
