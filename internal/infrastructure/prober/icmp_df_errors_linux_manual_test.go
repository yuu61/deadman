//go:build linux && manual

package prober

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const icmpFragNeeded = 4

// Inject errors on loopback so the test needs CAP_NET_RAW, but no routed test network.
// Only an error saying the datagram is too big is a result; any other error, and any error
// for another probe, is ignored while the raw socket still receives its reply.
func TestICMPQueuedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		// errors are injected in order, each as (ICMP code, whether it quotes our echo).
		errors []injectedError
		want   probe.ResultCode
	}{
		{"own frag needed", []injectedError{{icmpFragNeeded, true}}, probe.Unavailable},
		{"foreign frag needed", []injectedError{{icmpFragNeeded, false}}, probe.Success},
		{"own host unreachable", []injectedError{{1, true}}, probe.Success},
		{"own port unreachable", []injectedError{{3, true}}, probe.Success},
		{"foreign host unreachable", []injectedError{{1, false}}, probe.Success},
		// The queue holds the other row's error ahead of ours: every entry must be read.
		{
			"own frag needed behind foreign errors",
			[]injectedError{{1, false}, {3, false}, {icmpFragNeeded, true}},
			probe.Unavailable,
		},
		{
			"own frag needed behind own unreachable",
			[]injectedError{{1, true}, {icmpFragNeeded, true}},
			probe.Unavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := net.IPv4(127, 0, 0, 1)
			pr := icmpProbe{
				fam: familyOf(peer), privileged: true, df: true,
				id: nextProbeID(), seq: 1, token: echoPayload(probe.DirectPayloadSize),
			}
			p := &icmpPinger{echo: probe.EchoOptions{DF: true}}

			conn, err := p.dial(pr.fam, unix.SOCK_RAW, pr.id)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { discard(conn.f.Close()) }()

			err = conn.f.SetDeadline(time.Now().Add(icmpTimeout))
			if err != nil {
				t.Fatal(err)
			}

			var config net.ListenConfig

			injector, err := config.ListenPacket(t.Context(), "ip4:icmp", peer.String())
			if err != nil {
				t.Fatal(err)
			}
			defer injector.Close()

			start := time.Now()

			for _, injected := range tc.errors {
				quoted := pr
				if !injected.own {
					quoted.id = nextProbeID()
				}

				injectICMP(t, injector, peer, icmp.Message{
					Type: ipv4.ICMPTypeDestinationUnreachable, Code: injected.code,
					Body: &icmp.RawBody{Data: unreachableBody(quotedIPv4Echo(t, quoted, peer))},
				})
			}

			injectICMP(t, injector, peer, icmp.Message{
				Type: pr.fam.replyType,
				Body: &icmp.Echo{ID: pr.id, Seq: pr.seq, Data: pr.token},
			})

			if result := pr.recv(conn.rc, peer, start); result.Code != tc.want {
				t.Fatalf("result = %+v, want %v", result, tc.want)
			}
		})
	}
}

type injectedError struct {
	code int
	own  bool
}

// unreachableBody is a Destination Unreachable's body: two unused bytes, a next-hop MTU, and
// the quoted packet. The kernel caches an MTU a Frag needed reports for the route, and loopback
// is every probe's route, so the MTU is one just under loopback's: a smaller one would make
// later jumbo probes on this host fail until the cache entry expires.
func unreachableBody(quoted []byte) []byte {
	const nextHopMTU = 65535

	body := make([]byte, 2, 4+len(quoted)) // The two unused bytes.
	body = binary.BigEndian.AppendUint16(body, nextHopMTU)

	return append(body, quoted...)
}

func quotedIPv4Echo(t *testing.T, pr icmpProbe, peer net.IP) []byte {
	t.Helper()

	request, err := pr.build()
	if err != nil {
		t.Fatal(err)
	}

	header := ipv4.Header{
		Version: ipv4.Version, Len: ipv4.HeaderLen, TotalLen: ipv4.HeaderLen + len(request),
		TTL: 64, Protocol: unix.IPPROTO_ICMP, Src: peer, Dst: peer,
	}

	data, err := header.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	return append(data, request...)
}

func injectICMP(t *testing.T, conn net.PacketConn, peer net.IP, message icmp.Message) {
	t.Helper()

	data, err := message.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = conn.WriteTo(data, &net.IPAddr{IP: peer})
	if err != nil {
		t.Fatal(err)
	}
}
