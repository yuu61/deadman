package prober

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/net/icmp"
)

func TestEchoWaitStopsOnCancellation(t *testing.T) {
	conn := echoTestSocket(t)
	defer func() { discard(conn.Close()) }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()

	w := echoWaiter{conn: conn, kind: echoKindV4}
	start := time.Now()

	ok, err := w.wait(ctx, net.ParseIP("127.0.0.1"), 1, 1, nil, start.Add(5*time.Second))
	if ok || !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("canceled wait: ok=%v error=%v elapsed=%s", ok, err, time.Since(start))
	}
}

func echoTestSocket(t *testing.T) *icmp.PacketConn {
	t.Helper()

	conn, err := icmp.ListenPacket("udp4", "127.0.0.1")
	if err != nil {
		conn, err = icmp.ListenPacket("ip4:icmp", "127.0.0.1")
		if err != nil {
			t.Skipf("ICMP socket unavailable: %v", err)
		}
	}

	return conn
}

// Both timers can win the race, but only the probe's own response deadline is loss.
func TestEchoWaitDistinguishesProbeAndCallerDeadlines(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cause error
		want  error
	}{
		{"probe", errNexthopTimeout, nil},
		{"caller", context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for range 30 {
				conn := echoTestSocket(t)
				w := echoWaiter{conn: conn, kind: echoKindV4}
				deadline := time.Now().Add(time.Millisecond)
				parent, cancelParent := context.WithDeadlineCause(t.Context(), deadline, tt.cause)
				ctx, cancel := context.WithTimeoutCause(parent, icmpTimeout, errNexthopTimeout)

				ok, err := w.wait(ctx, net.ParseIP("127.0.0.1"), 1, 1, nil, deadline)

				cancel()
				cancelParent()
				discard(conn.Close())

				if ok || !errors.Is(err, tt.want) {
					t.Fatalf("deadline wait: ok=%v error=%v, want error=%v", ok, err, tt.want)
				}
			}
		})
	}
}
