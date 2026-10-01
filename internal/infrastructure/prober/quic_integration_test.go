package prober

import (
	"context"
	"crypto/tls"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Observe protocol closure even when it precedes Listener.Accept. A successful probe
// closes immediately, and quic-go may discard that connection from its accept queue.
type quicCloseTrace struct {
	closed chan<- qlog.ConnectionClosed
}

func (tr *quicCloseTrace) AddProducer() qlogwriter.Recorder { return tr }
func (*quicCloseTrace) SupportsSchemas(string) bool         { return true }
func (*quicCloseTrace) Close() error                        { return nil }

func (tr *quicCloseTrace) RecordEvent(event qlogwriter.Event) {
	if closed, ok := event.(qlog.ConnectionClosed); ok {
		select {
		case tr.closed <- closed:
		default:
		}
	}
}

func quicLoopback(t *testing.T) (string, <-chan qlog.ConnectionClosed) {
	t.Helper()

	cert := httptest.NewTLSServer(http.NotFoundHandler())
	certificates := cert.TLS.Certificates
	cert.Close()

	closed := make(chan qlog.ConnectionClosed, 1)

	listener, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: certificates,
		NextProtos:   []string{"deadman-test"},
	}, &quic.Config{
		Tracer: func(context.Context, bool, quic.ConnectionID) qlogwriter.Trace {
			return &quicCloseTrace{closed: closed}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)

		for {
			conn, acceptErr := listener.Accept(ctx)
			if acceptErr != nil {
				return
			}

			select {
			case <-conn.Context().Done():
			case <-ctx.Done():
				discard(conn.CloseWithError(0, "test finished"))
			}
		}
	}()

	t.Cleanup(func() {
		cancel()

		err := listener.Close()
		if err != nil {
			t.Error(err)
		}

		awaitProbeSignal(t, stopped)
	})

	return listener.Addr().String(), closed
}

func loopbackQUICPinger(t *testing.T, address string, params probe.QUIC) probe.Pinger {
	t.Helper()

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}

	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	params.Port = probe.PortNumber(number)

	p, err := New(compiled(t, probe.Spec{Addr: host, Params: params}), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	return p
}

// The real handshake succeeds repeatedly, and each connection is closed by the client
// before any fixture cleanup. An idle timeout must not mask a missing CloseWithError.
func TestQUICLoopbackReleasesEachConnection(t *testing.T) {
	address, closed := quicLoopback(t)
	p := loopbackQUICPinger(t, address, probe.QUIC{ALPN: "deadman-test"})

	const rounds = 12

	for round := range rounds {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		result := p.Send(ctx)

		cancel()

		if !result.IsSuccess() || result.RTT < 0 || math.IsNaN(result.RTT) ||
			math.IsInf(result.RTT, 0) {
			t.Fatalf("round %d: handshake result = %+v", round, result)
		}

		select {
		case event := <-closed:
			if event.Initiator != qlog.InitiatorRemote {
				t.Fatalf("round %d: connection was not closed by the probe: %+v", round, event)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: successful probe left its connection open", round)
		}
	}
}

func TestQUICLoopbackHandshakeErrorsAreUnobserved(t *testing.T) {
	for _, c := range []struct {
		name   string
		params probe.QUIC
	}{
		{"untrusted_certificate", probe.QUIC{ALPN: "deadman-test", Verify: probe.VerifyEnabled}},
		{"incompatible_alpn", probe.QUIC{ALPN: "other-protocol"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			address, _ := quicLoopback(t)
			p := loopbackQUICPinger(t, address, c.params)

			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()

			assertUnobservedProbe(t, p.Send(ctx), probe.Unavailable)
		})
	}
}

// Keep the UDP socket open and consume the Initial without answering it. This avoids
// both external routing dependencies and an OS-dependent connection-refused error.
func TestQUICUnansweredHandshakeHonorsCallerLifetime(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}

		t.Run(name, func(t *testing.T) {
			var listen net.ListenConfig

			socket, err := listen.ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { discard(socket.Close()) })

			p := loopbackQUICPinger(t, socket.LocalAddr().String(), probe.QUIC{})

			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
			}
			defer cancel()

			results := make(chan probe.Result, 1)
			go func() { results <- p.Send(ctx) }()

			err = socket.SetReadDeadline(time.Now().Add(3 * time.Second))
			if err != nil {
				t.Fatal(err)
			}

			packet := make([]byte, 2048)

			_, _, err = socket.ReadFrom(packet)
			if err != nil {
				t.Fatalf("probe sent no QUIC Initial: %v", err)
			}

			if !deadline {
				cancel()
				assertUnobservedProbe(t, awaitProbeResult(ctx, t, results), probe.Unavailable)

				return
			}

			result := awaitProbeResult(ctx, t, results)
			if result.Code != probe.Failed {
				t.Fatalf("unanswered handshake = %+v, want target timeout", result)
			}

			target := monitor.NewTarget("row#1", "target", "127.0.0.1")
			target.Consume(result)

			if got := target.Snapshot(); got.State != monitor.Down || got.Snt != 1 ||
				got.Loss != 1 {
				t.Fatalf("unanswered handshake was not counted as loss: %+v", got)
			}
		})
	}
}
