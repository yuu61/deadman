package prober

import (
	"net"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestJumboEchoReplyMatching(t *testing.T) {
	for _, tc := range []struct {
		peer string
		kind echoReplyKind
		size int
	}{
		{"192.0.2.1", echoKindV4, 8972},
		{"2001:db8::1", echoKindV6, 8952},
	} {
		payload := echoPayload(tc.size)
		if len(payload) != tc.size {
			t.Fatalf("payload size = %d, want %d", len(payload), tc.size)
		}

		message := icmp.Message{
			Type: tc.kind.replyType,
			Body: &icmp.Echo{ID: 7, Seq: 1, Data: payload},
		}

		data, err := message.Marshal(nil)
		if err != nil {
			t.Fatal(err)
		}

		if len(data) != tc.size+8 || echoReceiveSize(tc.size) < len(data)+ipv4.HeaderLen {
			t.Fatalf("buffer would truncate %d-byte echo", len(data))
		}

		peer := net.ParseIP(tc.peer)

		from := &net.IPAddr{IP: peer}
		if !nativeEchoReply(data, from, peer, 7, payload, true, tc.kind) {
			t.Fatalf("%s: jumbo reply rejected", tc.peer)
		}

		data[len(data)-1] ^= 1
		if nativeEchoReply(data, from, peer, 7, payload, true, tc.kind) {
			t.Fatal("reply with altered payload tail accepted")
		}
	}
}

func TestNativeEchoReplyIdentity(t *testing.T) {
	payload := echoPayload(24)
	peer := net.ParseIP("2001:db8::1")
	from := &net.IPAddr{IP: peer}
	message := icmp.Message{
		Type: ipv6.ICMPTypeEchoReply,
		Body: &icmp.Echo{ID: 9, Seq: 1, Data: payload},
	}

	data, err := message.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}

	if nativeEchoReply(data, from, peer, 8, payload, true, echoKindV6) {
		t.Fatal("raw reply with another identifier accepted")
	}

	if !nativeEchoReply(data, from, peer, 8, payload, false, echoKindV6) {
		t.Fatal("datagram reply with a kernel-assigned identifier rejected")
	}

	if nativeEchoReply(data, from, net.ParseIP("2001:db8::2"), 9, payload, true, echoKindV6) {
		t.Fatal("reply from another address accepted")
	}
}

func TestPingMTUErrorsAreUnobserved(t *testing.T) {
	summary := "1 packets transmitted, 0 received, 100% packet loss"
	for _, out := range []output{
		{stdout: summary, stderr: "ping: local error: message too long, mtu=1500"},
		{stdout: "From 192.0.2.254: Frag needed and DF set\n" + summary},
		{stdout: "1 packets transmitted, 0 received, +1 errors, 100% packet loss"},
		{stdout: summary, stderr: "ping: sendto: Message too long"},
		{stdout: "1 packets transmitted, 0 packets received, 100% packet loss", stderr: "packet too big"},
	} {
		if result := parsePingResult(out); result.Code != probe.Unavailable {
			t.Fatalf("MTU error counted as loss: %+v -> %+v", out, result)
		}
	}
}
