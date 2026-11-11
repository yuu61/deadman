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

func (fakeLink) send(*net.Interface, net.HardwareAddr, uint16, []byte) (time.Time, error) {
	return time.Now(), nil
}

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

// A slow gateway lookup must not shorten the reply wait: the echo still waits its full
// icmpTimeout after the lookup, rather than whatever the lookup left of one shared second.
func TestNexthopReplyWaitStartsAfterGatewayLookup(t *testing.T) {
	listener, err := (echoIPv4{}).listen(net.ParseIP("127.0.0.1"), 1)
	if err != nil {
		t.Skipf("raw ICMP socket unavailable: %v", err)
	}

	discard(listener.close())

	const lookup = 600 * time.Millisecond

	p := loopbackPinger(t, fakeLink{resolve: func() (net.HardwareAddr, error) {
		<-time.After(lookup)

		return net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil
	}})

	start := time.Now()
	result := p.Send(t.Context())
	elapsed := time.Since(start)

	if result.Code != probe.Failed || elapsed < lookup+icmpTimeout-100*time.Millisecond {
		t.Fatalf("result %+v after %v; want a loss after the lookup plus a full %v wait",
			result, elapsed, icmpTimeout)
	}
}

// recordingWaiter answers a forced echo at once and records what it was asked to wait for.
type recordingWaiter struct {
	echo     sentEcho
	deadline time.Time
}

func (w *recordingWaiter) wait(
	_ context.Context,
	echo sentEcho,
	deadline time.Time,
) (float64, bool, error) {
	w.echo, w.deadline = echo, deadline

	return 0.25, true, nil
}

func (*recordingWaiter) close() error { return nil }

type recordingFamily struct {
	echoIPv4

	waiter *recordingWaiter
}

func (f recordingFamily) listen(net.IP, int) (replyWaiter, error) { return f.waiter, nil }

// sentAtLink reports its frames handed to the kernel at a fixed instant.
type sentAtLink struct {
	fakeLink

	at time.Time
}

func (l sentAtLink) send(*net.Interface, net.HardwareAddr, uint16, []byte) (time.Time, error) {
	return l.at, nil
}

// Without a socket: the reply wait gets its full icmpTimeout after a slow gateway lookup,
// and the RTT starts at the instant the transport handed the frame to the kernel.
func TestNexthopReplyWaitIsTimedFromTheSend(t *testing.T) {
	const lookup = 300 * time.Millisecond

	var resolved time.Time

	sent := time.Now().Add(time.Hour) // distinct from any instant the pinger could take itself.
	link := sentAtLink{at: sent, fakeLink: fakeLink{resolve: func() (net.HardwareAddr, error) {
		<-time.After(lookup)

		resolved = time.Now()

		return net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil
	}}}
	waiter := &recordingWaiter{}

	p := loopbackPinger(t, link)

	result := p.sendForced(t.Context(), recordingFamily{waiter: waiter}, net.ParseIP("127.0.0.1"))
	if !result.IsSuccess() || result.RTT != 0.25 {
		t.Fatalf("result = %+v, want the waiter's 0.25 ms reply", result)
	}

	if !waiter.echo.at.Equal(sent) {
		t.Errorf("RTT starts at %v, want the transport's send instant %v", waiter.echo.at, sent)
	}

	if wait := waiter.deadline.Sub(resolved); wait < icmpTimeout-50*time.Millisecond {
		t.Errorf("reply wait %v after the gateway lookup, want the full %v", wait, icmpTimeout)
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
