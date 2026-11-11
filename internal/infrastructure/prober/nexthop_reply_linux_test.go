//go:build linux

package prober

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// The forced probe's reply matcher (the direct prober's, on its raw socket) accepts only
// the reply to its own probe in either family: the right peer, id, seq and token, and an
// echo reply rather than a request.
func TestForcedReplyMatch(t *testing.T) {
	const (
		id  = 0xabcd
		seq = 5
	)

	for _, fam := range []struct {
		name            string
		family          probe.Family
		peer, otherPeer string
		reply, request  icmp.Type
	}{
		{
			"IPv4", probe.FamilyIPv4, "203.0.113.9", "198.51.100.1",
			ipv4.ICMPTypeEchoReply, ipv4.ICMPTypeEcho,
		},
		{
			"IPv6", probe.FamilyIPv6, "2001:db8::9", "2001:db8::a",
			ipv6.ICMPTypeEchoReply, ipv6.ICMPTypeEchoRequest,
		},
	} {
		t.Run(fam.name, func(t *testing.T) {
			token := []byte{0x01, 0x02, 0x03, 0x04}
			peer := net.ParseIP(fam.peer)
			reply := marshalEcho(t, fam.reply, id, seq, token)

			probeOf := func(id, seq int, token []byte) icmpProbe {
				return icmpProbe{
					fam: icmpFamilyFor(
						fam.family,
					),
					privileged: true,
					id:         id,
					seq:        seq,
					token:      token,
				}
			}

			if !probeOf(id, seq, token).match(reply, sockaddrOf(t, peer), peer) {
				t.Fatal("the matching reply was rejected")
			}

			// A reply from a different host, with a different id/seq, or with the right
			// id/seq but another process's token, is rejected; so is an echo request.
			for what, c := range map[string]struct {
				b       []byte
				from    unix.Sockaddr
				id, seq int
				token   []byte
			}{
				"from the wrong source": {reply, sockaddrOf(t, net.ParseIP(fam.otherPeer)), id, seq, token},
				"with the wrong id":     {reply, sockaddrOf(t, peer), id + 1, seq, token},
				"with the wrong seq":    {reply, sockaddrOf(t, peer), id, seq + 1, token},
				"with the wrong token": {
					reply, sockaddrOf(t, peer), id, seq, []byte{0x09, 0x09, 0x09, 0x09},
				},
				"that is a request": {
					marshalEcho(t, fam.request, id, seq, token), sockaddrOf(t, peer), id, seq, token,
				},
			} {
				if probeOf(c.id, c.seq, c.token).match(c.b, c.from, peer) {
					t.Errorf("matched a reply %s", what)
				}
			}
		})
	}
}

func sockaddrOf(t *testing.T, ip net.IP) unix.Sockaddr {
	t.Helper()

	sa, err := ipSockaddr(ip, 0)
	if err != nil {
		t.Fatal(err)
	}

	return sa
}

// marshalEcho is an echo message of type typ carrying id, seq and token.
func marshalEcho(t *testing.T, typ icmp.Type, id, seq int, token []byte) []byte {
	t.Helper()

	b, err := (&icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: token}}).Marshal(
		nil,
	)
	if err != nil {
		t.Fatalf("marshal %v: %v", typ, err)
	}

	return b
}

// replyTestWaiter is a forced probe's reply socket on loopback; it needs a raw ICMP socket.
func replyTestWaiter(t *testing.T) replyWaiter {
	t.Helper()

	w, err := listenReplies(probe.FamilyIPv4, net.ParseIP("127.0.0.1"), 1)
	if err != nil {
		t.Skipf("raw ICMP socket unavailable: %v", err)
	}

	return w
}

func unansweredEcho() sentEcho {
	return sentEcho{peer: net.ParseIP("127.0.0.1"), id: 1, seq: 1, at: time.Now()}
}

func TestEchoWaitStopsOnCancellation(t *testing.T) {
	w := replyTestWaiter(t)
	defer func() { discard(w.close()) }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()

	start := time.Now()

	_, ok, err := w.wait(ctx, unansweredEcho(), start.Add(5*time.Second))
	if ok || !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("canceled wait: ok=%v error=%v elapsed=%s", ok, err, time.Since(start))
	}
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
				w := replyTestWaiter(t)
				deadline := time.Now().Add(time.Millisecond)
				parent, cancelParent := context.WithDeadlineCause(t.Context(), deadline, tt.cause)
				ctx, cancel := context.WithTimeoutCause(parent, icmpTimeout, errNexthopTimeout)

				_, ok, err := w.wait(ctx, unansweredEcho(), deadline)

				cancel()
				cancelParent()
				discard(w.close())

				if ok || !errors.Is(err, tt.want) {
					t.Fatalf("deadline wait: ok=%v error=%v, want error=%v", ok, err, tt.want)
				}
			}
		})
	}
}
