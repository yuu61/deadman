package prober

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestIPChecksum(t *testing.T) {
	// Canonical IPv4 header (checksum field zeroed) from the Wikipedia worked
	// example; the correct header checksum is 0xb1e6.
	header := []byte{
		0x45, 0x00, 0x00, 0x3c, 0x1c, 0x46, 0x40, 0x00,
		0x40, 0x06, 0x00, 0x00, 0xac, 0x10, 0x0a, 0x63,
		0xac, 0x10, 0x0a, 0x0c,
	}

	if got := ipChecksum(header); got != 0xb1e6 {
		t.Errorf("ipChecksum = %#04x, want 0xb1e6", got)
	}

	// With the checksum filled in, the checksum over the whole header is 0.
	header[10] = 0xb1
	header[11] = 0xe6

	if got := ipChecksum(header); got != 0 {
		t.Errorf("ipChecksum over a valid header = %#04x, want 0", got)
	}
}

func TestEchoIPv4Build(t *testing.T) {
	src := net.ParseIP("192.0.2.1")
	dst := net.ParseIP("198.51.100.2")
	token := []byte{0xde, 0xad, 0xbe, 0xef}

	pkt, err := echoIPv4{}.build(src, dst, 0x1234, 7, token)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if len(pkt) < 20+8 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}

	// The IPv4 header checksum must be valid (sum over the header is 0).
	if got := ipChecksum(pkt[:20]); got != 0 {
		t.Errorf("IPv4 header checksum invalid: residual %#04x", got)
	}

	// Source/destination land in the header.
	if !net.IP(pkt[12:16]).Equal(src.To4()) || !net.IP(pkt[16:20]).Equal(dst.To4()) {
		t.Errorf(
			"header src/dst = %v/%v, want %v/%v",
			net.IP(pkt[12:16]),
			net.IP(pkt[16:20]),
			src,
			dst,
		)
	}

	// Protocol is ICMP and DF is not set.
	if pkt[9] != ianaProtocolICMP {
		t.Errorf("protocol = %d, want %d", pkt[9], ianaProtocolICMP)
	}

	if pkt[6]&0x40 != 0 {
		t.Errorf("Don't-Fragment bit unexpectedly set: flags byte %#02x", pkt[6])
	}

	// The ICMP payload parses back to our echo request.
	msg, err := icmp.ParseMessage(ianaProtocolICMP, pkt[20:])
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		t.Fatalf("body type = %T, want *icmp.Echo", msg.Body)
	}

	if echo.ID != 0x1234 || echo.Seq != 7 {
		t.Errorf("echo id/seq = %d/%d, want 4660/7", echo.ID, echo.Seq)
	}

	if !bytes.Equal(echo.Data, token) {
		t.Errorf("echo data = %x, want %x", echo.Data, token)
	}
}

func TestPickOnLinkSrc(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.1.10"), Mask: net.CIDRMask(24, 32)},
	}

	src, ok := pickOnLinkSrc(addrs, net.ParseIP("192.168.1.1"))
	if !ok || !src.Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("pickOnLinkSrc = %v, %v; want 192.168.1.10, true", src, ok)
	}

	if _, ok := pickOnLinkSrc(addrs, net.ParseIP("172.16.0.1")); ok {
		t.Error("pickOnLinkSrc matched an off-link gateway")
	}
}

func TestAddrsHaveIP(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)},
	}

	if !addrsHaveIP(addrs, net.ParseIP("10.0.0.5")) {
		t.Error("addrsHaveIP should find 10.0.0.5")
	}

	if addrsHaveIP(addrs, net.ParseIP("10.0.0.6")) {
		t.Error("addrsHaveIP should not find 10.0.0.6")
	}
}

func TestSelectEgressErrors(t *testing.T) {
	v4dst := net.ParseIP("198.51.100.2")
	v6dst := net.ParseIP("2001:db8::2")

	// A non-existent interface name is rejected for both families. (A link-local IPv6
	// gateway without an interface is Compile's to refuse, so no plan brings one here.)
	noIface := probe.SourceInterface("deadman-no-such-iface0")

	_, _, err := selectEgress(t.Context(), net.ParseIP("192.0.2.1"), noIface, v4dst)
	if err == nil {
		t.Error("IPv4 selectEgress with an unknown interface should error")
	}

	_, _, err = selectEgress(t.Context(), net.ParseIP("2001:db8::1"), noIface, v6dst)
	if err == nil {
		t.Error("IPv6 selectEgress with an unknown interface should error")
	}
}

// Each family's reply matcher accepts only the reply to its own probe: the right peer,
// id, seq and token, and an echo reply rather than a request.
func TestMatchEchoReply(t *testing.T) {
	const (
		id  = 0xabcd
		seq = 5
	)

	for _, fam := range []struct {
		name            string
		peer, otherPeer string
		reply, request  icmp.Type
		kind            echoReplyKind
	}{
		{
			"IPv4", "203.0.113.9", "198.51.100.1",
			ipv4.ICMPTypeEchoReply, ipv4.ICMPTypeEcho, echoKindV4,
		},
		{
			"IPv6", "2001:db8::9", "2001:db8::a",
			ipv6.ICMPTypeEchoReply, ipv6.ICMPTypeEchoRequest, echoKindV6,
		},
	} {
		t.Run(fam.name, func(t *testing.T) {
			token := []byte{0x01, 0x02, 0x03, 0x04}
			peer := net.ParseIP(fam.peer)
			src := &net.IPAddr{IP: peer}
			reply := marshalEcho(t, fam.reply, id, seq, token)

			if !matchEcho(reply, src, peer, id, seq, token, fam.kind) {
				t.Fatal("the matching reply was rejected")
			}

			// A reply from a different host, with a different id/seq, or with the right
			// id/seq but another process's token, is rejected; so is an echo request.
			for what, c := range map[string]struct {
				b       []byte
				src     net.Addr
				id, seq int
				token   []byte
			}{
				"from the wrong source": {reply, &net.IPAddr{IP: net.ParseIP(fam.otherPeer)}, id, seq, token},
				"with the wrong id":     {reply, src, id + 1, seq, token},
				"with the wrong seq":    {reply, src, id, seq + 1, token},
				"with the wrong token":  {reply, src, id, seq, []byte{0x09, 0x09, 0x09, 0x09}},
				"that is a request":     {marshalEcho(t, fam.request, id, seq, token), src, id, seq, token},
			} {
				if matchEcho(c.b, c.src, peer, c.id, c.seq, c.token, fam.kind) {
					t.Errorf("matched a reply %s", what)
				}
			}
		})
	}
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

func TestNextProbeIDIs16Bit(t *testing.T) {
	for range 3 {
		if id := nextProbeID(); id < 0 || id > 0xffff {
			t.Fatalf("nextProbeID = %d, want a 16-bit value", id)
		}
	}
}

// The pinger forces the probe through the gateway the plan compiled, in its family: an
// IPv4-mapped gateway is forced as IPv4.
func TestNewNexthopPingerTakesPlanGateway(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("next-hop forcing is Linux's alone")
	}

	for gateway, want := range map[string]probe.Family{
		"192.0.2.254": probe.FamilyIPv4, "::ffff:192.0.2.254": probe.FamilyIPv4, "2001:db8::ffff": probe.FamilyIPv6,
	} {
		p, err := New(
			compiled(
				t,
				probe.Spec{
					Addr:   "example.com",
					Params: probe.Nexthop{Gateway: netip.MustParseAddr(gateway)},
				},
			), "row#1",
		)
		if err != nil {
			t.Fatalf("nexthop=%s: %v", gateway, err)
		}

		np, ok := p.(*nexthopPinger)
		if !ok {
			t.Fatalf("nexthop=%s: New returned %T, want *nexthopPinger", gateway, p)
		}

		if np.family != want || !np.nexthopIP.Equal(net.ParseIP(gateway)) {
			t.Errorf(
				"nexthop=%s: gateway %v in %v, want it in %v",
				gateway,
				np.nexthopIP,
				np.family,
				want,
			)
		}
	}
}

// fakeLink is a link transport whose gateway resolution a test scripts. Its send puts
// nothing on the wire but reports success, so an echo it "sent" is never answered.
type fakeLink struct {
	resolve func() (net.HardwareAddr, error)
}

func (f fakeLink) resolveGateway(
	context.Context,
	*net.Interface,
	net.IP,
) (net.HardwareAddr, error) {
	return f.resolve()
}

func (fakeLink) send(*net.Interface, net.HardwareAddr, uint16, []byte) error { return nil }

// loopbackPinger is a forced IPv4 probe out this host's loopback, whose own address
// stands in for an on-link gateway, over link.
func loopbackPinger(t *testing.T, link linkTransport) *nexthopPinger {
	t.Helper()

	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip(err)
	}

	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 && ifi.Flags&net.FlagUp != 0 {
			return &nexthopPinger{
				dest:      compiled(t, probe.Spec{Addr: "127.0.0.1"}).Destination(),
				nexthopIP: net.ParseIP("127.0.0.1").To4(),
				family:    probe.FamilyIPv4,
				source:    probe.SourceInterface(ifi.Name),
				transport: link,
			}
		}
	}

	t.Skip("no loopback interface")

	return nil
}

// A gateway that never answers ARP/NDP is the forced path down at its first hop, which a
// plain probe through it would find as a timeout: target loss, not an unknown. Any other
// failure to resolve it observed nothing.
func TestNexthopDeadGatewayIsLoss(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want probe.ResultCode
	}{
		{"unresolved gateway", fmt.Errorf("%w: 192.0.2.254 on eth1", errGatewayUnresolved), probe.Failed},
		{"other failure", errors.New("netlink: permission denied"), probe.Unavailable},
	} {
		p := loopbackPinger(
			t,
			fakeLink{resolve: func() (net.HardwareAddr, error) { return nil, c.err }},
		)

		if res := p.Send(t.Context()); res.Code != c.want {
			t.Errorf("%s: Code = %v, want %v", c.name, res.Code, c.want)
		}
	}
}

// A probe whose gateway resolution outlasted its time sends no echo: none could be waited
// for, so it observed nothing, rather than counting a loss. (Without the raw socket the
// listener cannot open either, which also observes nothing; as root this tells the two
// apart.)
func TestNexthopOutOfTimeSendsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	p := loopbackPinger(t, fakeLink{resolve: func() (net.HardwareAddr, error) {
		<-ctx.Done()

		return net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil
	}})

	if res := p.Send(ctx); res.Code != probe.Unavailable {
		t.Errorf("Code = %v, want Unavailable", res.Code)
	}
}

type noListenFamily struct {
	echoIPv4

	t *testing.T
}

func (f noListenFamily) listen(net.IP, int) (replyWaiter, error) {
	f.t.Error("canceled probe must not open a reply socket")

	return nil, context.Canceled
}

func TestNexthopCanceledResolutionSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	p := loopbackPinger(t, fakeLink{resolve: func() (net.HardwareAddr, error) {
		cancel()

		return net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil
	}})

	result := p.sendForced(ctx, noListenFamily{t: t}, net.ParseIP("127.0.0.1"))
	if result.Code != probe.Unavailable {
		t.Errorf("canceled resolution: Code=%v, want Unavailable", result.Code)
	}
}

func TestNexthopUnansweredEchoCountsAsLoss(t *testing.T) {
	listener, err := (echoIPv4{}).listen(net.ParseIP("127.0.0.1"), 1)
	if err != nil {
		t.Skipf("raw ICMP socket unavailable: %v", err)
	}

	discard(listener.close())

	p := loopbackPinger(t, fakeLink{resolve: func() (net.HardwareAddr, error) {
		return net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil
	}})

	result := p.Send(t.Context())
	if result.Code != probe.Failed {
		t.Fatalf("unanswered echo: Code=%v, want Failed", result.Code)
	}

	target := monitor.NewTarget("row", "host", "127.0.0.1")
	target.Consume(result)

	stats := target.Snapshot().Stats
	if stats.Snt != 1 || stats.Loss != 1 {
		t.Errorf("unanswered echo was not counted as loss: %+v", stats)
	}
}

func TestGatewayLookupErrorInvalidatesEgress(t *testing.T) {
	calls := 0
	p := loopbackPinger(t, fakeLink{resolve: func() (net.HardwareAddr, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("interface removed")
		}

		return net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil
	}})

	current, err := net.InterfaceByName(p.source.String())
	if err != nil {
		t.Fatal(err)
	}

	stale := *current
	stale.Index = 999999
	p.egress, p.srcIP = &stale, net.ParseIP("127.0.0.1")

	_, err = p.resolve(t.Context(), net.ParseIP("127.0.0.1"))
	if err == nil || p.egress != nil {
		t.Fatal("failed lookup retained removed interface")
	}

	route, err := p.resolve(t.Context(), net.ParseIP("127.0.0.1"))
	if err != nil || route.iface.Index != current.Index {
		t.Fatalf("next round did not select live interface: %+v, %v", route, err)
	}
}
