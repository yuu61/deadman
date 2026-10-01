package prober

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestPortableLiteralPreservesZone(t *testing.T) {
	p := &icmpPinger{dest: compiled(t, probe.Spec{Addr: "fe80::1%en0"}).Destination()}

	resolved, err := p.resolvePortable(t.Context())
	if err != nil || resolved.Zone != "en0" || !resolved.IP.Equal(net.ParseIP("fe80::1")) {
		t.Fatalf("scoped literal: %v, %v", resolved, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if result := p.sendPortable(ctx); result.Code != probe.Unavailable {
		t.Fatalf("canceled probe = %+v", result)
	}
}

func TestScopedReplyRejectsUnrelatedEchoes(t *testing.T) {
	peer := net.ParseIP("fe80::2")
	token := []byte("our-token")
	reply := icmp.Message{
		Type: ipv6.ICMPTypeEchoReply,
		Body: &icmp.Echo{ID: 123, Seq: 1, Data: token},
	}

	data, err := reply.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}

	from := &net.IPAddr{IP: peer, Zone: "en0"}
	if !scopedReply(data, from, peer, 123, token, true) {
		t.Fatal("matching raw reply rejected")
	}

	if scopedReply(data, from, peer, 124, token, true) {
		t.Fatal("other raw identifier accepted")
	}

	if !scopedReply(data, from, peer, 124, token, false) {
		t.Fatal("kernel datagram identifier not accepted")
	}

	if scopedReply(data, from, peer, 123, []byte("other"), false) {
		t.Fatal("other probe payload accepted")
	}

	if scopedReply(data, from, net.ParseIP("fe80::3"), 123, token, false) {
		t.Fatal("other peer accepted")
	}
}

// Exercise the scoped adapter on a real socket when the host permits raw ICMP.
func TestPortableScopedLoopback(t *testing.T) {
	if !useICMPPrivileged() {
		t.Skip("raw ICMP unavailable")
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback == 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		source := netip.MustParseAddr("::1").WithZone(iface.Name)

		socket, err := icmp.ListenPacket("ip6:ipv6-icmp", source.String())
		if err != nil {
			t.Skipf("this OS cannot bind a scoped loopback source: %v", err)
		}

		err = socket.Close()
		if err != nil {
			t.Fatal(err)
		}

		p := &icmpPinger{
			dest:       compiled(t, probe.Spec{Addr: "::1"}).Destination(),
			source:     probe.SourceAddr(source),
			privileged: true,
		}
		if result := p.sendPortable(t.Context()); !result.IsSuccess() {
			t.Fatalf("scoped loopback: %+v", result)
		}

		return
	}

	t.Skip("no live loopback interface")
}
