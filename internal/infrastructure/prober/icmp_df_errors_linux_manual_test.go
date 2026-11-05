//go:build linux && manual

package prober

import (
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Inject errors on loopback so the test needs CAP_NET_RAW, but no routed test
// network. The raw socket must ignore a foreign error and still receive its reply.
func TestICMPForeignExtendedErrors(t *testing.T) {
	for _, code := range []int{1, 3} { // Host unreachable and port unreachable.
		for _, foreign := range []bool{false, true} {
			t.Run(strconv.Itoa(code)+"/foreign="+strconv.FormatBool(foreign), func(t *testing.T) {
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

				quoted := pr
				if foreign {
					quoted.id = nextProbeID()
				}

				data := quotedIPv4Echo(t, quoted, peer)
				start := time.Now()

				injectICMP(t, injector, peer, icmp.Message{
					Type: ipv4.ICMPTypeDestinationUnreachable, Code: code,
					Body: &icmp.DstUnreach{Data: data},
				})
				injectICMP(t, injector, peer, icmp.Message{
					Type: pr.fam.replyType,
					Body: &icmp.Echo{ID: pr.id, Seq: pr.seq, Data: pr.token},
				})

				want := probe.Unavailable
				if foreign {
					want = probe.Success
				}

				if result := pr.recv(conn.rc, peer, start); result.Code != want {
					t.Fatalf("result = %+v, want %v", result, want)
				}
			})
		}
	}
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
