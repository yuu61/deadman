package ping

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// The next-hop pinger forces a direct ICMP probe out through a chosen gateway
// (next-hop) instead of letting the kernel pick the route. Neither pro-bing nor a
// plain IP raw socket can override the next-hop (the kernel routes by destination
// IP), and mutating the routing table races with concurrent probes. The only
// clean, stateless way is to send the frame at L2 addressed to the gateway's MAC
// (see linkTransport); the reply returns by ordinary routing and is read back on a
// raw ICMP socket (see echoFamily).
//
// The address-family work sits behind one forward-looking seam, echoFamily: build
// the ICMP echo, open the raw listener, match the reply. IPv4 is implemented today
// (echoIPv4); IPv6/NDP can slot in as an echoIPv6 without touching the rest.
//
// The OS-specific half — putting an L3 packet on the wire toward a MAC and resolving
// that MAC from the kernel neighbor cache — is linkTransport (Linux AF_PACKET).
// Next-hop forcing is a Linux-only feature, like the other Linux-bound via modes
// (netns/vrf via `ip`, tcp via hping3): the AF_PACKET injection it needs has no
// portable equivalent, so the transport is build-tagged and non-Linux gets a stub
// that degrades the probe to a failure glyph. It is not a portability roadmap.
//
// Everything else (dispatch, egress selection, id/seq, timeout, caching, the
// resolve→build→send→recv orchestration) is family-agnostic and lives here.

var (
	errNoTransport = errors.New("nexthop: next-hop forcing is only supported on Linux")
	errBadGateway  = errors.New("nexthop: invalid IPv4 gateway address")
)

// linkTransport is the OS-specific half of next-hop forcing. The Linux AF_PACKET
// implementation lives in nexthop_transport_linux.go; nexthop_transport_other.go is
// a stub whose newLinkTransport returns nil on non-Linux, so the probe degrades to a
// failure glyph there. Receiving replies is cross-platform, so it is not here.
type linkTransport interface {
	// resolveGateway returns the link-layer address of nexthop as reached out
	// iface, consulting (and if needed populating) the OS neighbor cache.
	resolveGateway(iface *net.Interface, nexthop net.IP) (net.HardwareAddr, error)
	// send transmits an already-built L3 packet to dstMAC out iface. ethertype
	// selects the L3 protocol carried in the frame.
	send(iface *net.Interface, dstMAC net.HardwareAddr, ethertype uint16, l3 []byte) error
}

// echoFamily is the address-family-specific half: building the echo request,
// opening a raw ICMP listener, and matching the reply. IPv4 is implemented
// (echoIPv4); an echoIPv6 (NDP/ICMPv6) can be added without touching the core or
// the transport.
type echoFamily interface {
	ethertype() uint16
	build(src, dst net.IP, id, seq int, token []byte) ([]byte, error)
	listen(src net.IP) (replyWaiter, error)
}

// replyWaiter blocks for the echo reply to one probe. close releases the listener.
type replyWaiter interface {
	// wait reports the reply's TTL once an echo reply matching id/seq/token from
	// peer arrives, or ok=false once deadline passes with no match.
	wait(peer net.IP, id, seq int, token []byte, deadline time.Time) (ttl int, ok bool, err error)
	close() error
}

// resolved is the cached result of locating the gateway: where to send and as what.
type resolved struct {
	iface *net.Interface
	src   net.IP
	mac   net.HardwareAddr
}

// familyFor returns the forcing implementation for dst's address family, or nil
// when forcing is not implemented for it (IPv6 today), in which case the caller
// falls back to ordinary routing. Adding IPv6 forcing means returning an echoIPv6
// here.
func familyFor(dst net.IP) echoFamily {
	if dst.To4() != nil {
		return echoIPv4{}
	}

	return nil
}

// probeCounter hands out per-probe ICMP IDs. Under the raw-socket fan-out every
// listener sees every host's ICMP, so each in-flight probe needs a distinct id to
// disambiguate its own reply (together with the peer address).
var probeCounter atomic.Uint32

func nextProbeID() int {
	return int(uint16(probeCounter.Add(1))) //nolint:gosec // intentional wrap to a 16-bit ICMP id.
}

// nexthopPinger forces a direct ICMP probe to addr via the gateway nexthopIP. It
// is selected only on the default (direct-ICMP) path; relay/via/tcp take
// precedence.
type nexthopPinger struct {
	addr      string
	nexthopIP net.IP
	source    string
	transport linkTransport // nil on unsupported platforms.

	mu        sync.Mutex
	egress    *net.Interface
	srcIP     net.IP
	cachedMAC net.HardwareAddr
}

func newNexthopPinger(s Spec) (Pinger, error) {
	return &nexthopPinger{
		addr:      s.Addr,
		nexthopIP: net.ParseIP(s.Relay["nexthop"]),
		source:    s.Source,
		transport: newLinkTransport(),
	}, nil
}

func (p *nexthopPinger) Send(ctx context.Context) Result {
	// Bound the whole probe (DNS + ARP + echo) to icmpTimeout: the TUI passes
	// context.Background(), so without this an unresolvable name could block a
	// round far past the intended timeout.
	ctx, cancel := context.WithTimeout(ctx, icmpTimeout)
	defer cancel()

	dst := resolveIPv4Preferred(ctx, p.addr)
	if dst == nil {
		return Result{Code: Failed, TTL: -1}
	}

	fam := familyFor(dst)
	if fam == nil {
		// IPv6 (or any family without a forcing implementation): monitor via
		// ordinary routing. The startup check warns that nexthop is ignored here.
		return sendFallback(ctx, dst, p.source)
	}

	return p.sendForced(ctx, fam, dst)
}

// sendFallback probes dst via ordinary routing, for address families without a
// forcing implementation (IPv6 today). dst is already resolved, so the pinger is
// built from its literal address: passing the original hostname would make
// pro-bing run a second DNS lookup that ignores ctx and could outlast the timeout.
func sendFallback(ctx context.Context, dst net.IP, source string) Result {
	fb, err := newICMPPinger(Spec{Addr: dst.String(), Source: source})
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	return fb.Send(ctx)
}

// sendForced runs the resolve→listen→build→send→wait sequence for an IPv4 target.
func (p *nexthopPinger) sendForced(ctx context.Context, fam echoFamily, dst net.IP) Result {
	r, err := p.resolve()
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	waiter, err := fam.listen(r.src)
	if err != nil {
		p.reset() // the source IP may be gone (interface recreated); re-select next round.

		return Result{Code: Failed, TTL: -1}
	}
	defer func() { _ = waiter.close() }()

	id, seq, token := nextProbeID(), 1, newProbeToken()

	pkt, err := fam.build(r.src, dst, id, seq, token)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	deadline := probeDeadline(ctx)
	start := time.Now()

	err = p.transport.send(r.iface, r.mac, fam.ethertype(), pkt)
	if err != nil {
		p.reset() // the ifindex/MAC may be stale (interface or gateway changed); re-select.

		return Result{Code: Failed, TTL: -1}
	}

	ttl, ok, err := waiter.wait(dst, id, seq, token, deadline)
	if err != nil || !ok {
		p.invalidateMAC() // no reply: the gateway MAC may have changed; re-read the neighbor table next round.

		return Result{Code: Failed, TTL: -1}
	}

	rtt := float64(time.Since(start).Microseconds()) / usPerMs

	return Result{Success: true, Code: Success, RTT: rtt, TTL: ttl}
}

// resolve returns where to send and as what, caching the result across rounds. The
// lock serializes the (rare) overlapping probes of one target.
func (p *nexthopPinger) resolve() (resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.transport == nil {
		return resolved{}, errNoTransport
	}

	if p.nexthopIP == nil {
		return resolved{}, errBadGateway
	}

	if p.egress == nil {
		ifi, src, err := selectEgress(p.nexthopIP, p.source)
		if err != nil {
			return resolved{}, err
		}

		p.egress, p.srcIP = ifi, src
	}

	if p.cachedMAC == nil {
		mac, err := p.transport.resolveGateway(p.egress, p.nexthopIP)
		if err != nil {
			return resolved{}, err
		}

		p.cachedMAC = mac
	}

	return resolved{iface: p.egress, src: p.srcIP, mac: p.cachedMAC}, nil
}

func (p *nexthopPinger) invalidateMAC() {
	p.mu.Lock()
	p.cachedMAC = nil
	p.mu.Unlock()
}

// reset drops the whole cached resolution (egress, source, MAC) so the next probe
// re-selects from scratch. Used when a send/listen fails, which can mean the egress
// interface was recreated with a new ifindex or lost its address.
func (p *nexthopPinger) reset() {
	p.mu.Lock()
	p.egress = nil
	p.srcIP = nil
	p.cachedMAC = nil
	p.mu.Unlock()
}

// probeTokenLen is the size of the random per-probe token echoed in the ICMP data.
const probeTokenLen = 8

// newProbeToken returns a random token placed in the echo payload and verified in
// the reply. Because a raw ICMP socket also receives other processes' replies
// (whose ids/seqs collide with ours), the token is what distinguishes our probe
// from another deadman watching the same destination. It is not security-sensitive.
func newProbeToken() []byte {
	b := make([]byte, probeTokenLen)
	_, _ = rand.Read(
		b,
	) // crypto/rand.Read does not fail on supported platforms; a zero token still works.

	return b
}

// probeDeadline is the earlier of the per-probe timeout and any caller deadline.
func probeDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(icmpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return d
	}

	return deadline
}

// resolveIPv4Preferred parses or resolves addr, preferring an IPv4 result so a
// dual-stack name still takes the forcing path.
func resolveIPv4Preferred(ctx context.Context, addr string) net.IP {
	if ip := net.ParseIP(addr); ip != nil {
		return ip
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, addr)
	if err != nil || len(ips) == 0 {
		return nil
	}

	for _, ip := range ips {
		if ip.IP.To4() != nil {
			return ip.IP
		}
	}

	return ips[0].IP
}

// selectEgress picks the egress interface and source IP for reaching nexthop. The
// gateway must be on-link (directly connected); off-link gateways and a source not
// assigned to the egress interface are rejected so they fail at construction-of-
// state rather than silently dropping replies. source may be empty (auto-select),
// an interface name, or a source IP.
func selectEgress(nexthop net.IP, source string) (*net.Interface, net.IP, error) {
	if nexthop.To4() == nil {
		return nil, nil, fmt.Errorf("nexthop: %s is not an IPv4 address", nexthop)
	}

	if source != "" && net.ParseIP(source) == nil {
		return egressByName(source, nexthop)
	}

	return egressBySubnet(net.ParseIP(source), nexthop)
}

// egressByName resolves the egress from an explicit interface name.
func egressByName(name string, nexthop net.IP) (*net.Interface, net.IP, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, nil, fmt.Errorf("nexthop: interface %q: %w", name, err)
	}

	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, nil, err
	}

	src, ok := pickOnLinkSrc(addrs, nexthop)
	if !ok {
		return nil, nil, fmt.Errorf("nexthop: gateway %s is not on-link on %s", nexthop, name)
	}

	return ifi, src, nil
}

// egressBySubnet finds the interface whose connected subnet contains nexthop,
// optionally constrained to one carrying wantSrc.
func egressBySubnet(wantSrc, nexthop net.IP) (*net.Interface, net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}

	for i := range ifaces {
		if src, ok := egressCandidate(&ifaces[i], wantSrc, nexthop); ok {
			return &ifaces[i], src, nil
		}
	}

	if wantSrc != nil {
		return nil, nil, fmt.Errorf(
			"nexthop: no interface has source %s with gateway %s on-link",
			wantSrc,
			nexthop,
		)
	}

	return nil, nil, fmt.Errorf("nexthop: gateway %s is not on-link on any interface", nexthop)
}

// egressCandidate reports whether ifi can reach nexthop on-link and, if so, the
// source IP to use (honoring wantSrc when set).
func egressCandidate(ifi *net.Interface, wantSrc, nexthop net.IP) (net.IP, bool) {
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
		return nil, false
	}

	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, false
	}

	src, ok := pickOnLinkSrc(addrs, nexthop)
	if !ok {
		return nil, false
	}

	if wantSrc != nil && !wantSrc.Equal(src) {
		if !addrsHaveIP(addrs, wantSrc) {
			return nil, false
		}

		return wantSrc, true
	}

	return src, true
}

// pickOnLinkSrc returns the IPv4 address among addrs whose subnet contains
// nexthop, i.e. the local address from which nexthop is directly reachable.
func pickOnLinkSrc(addrs []net.Addr, nexthop net.IP) (net.IP, bool) {
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.To4() == nil {
			continue
		}

		if ipnet.Contains(nexthop) {
			return ipnet.IP.To4(), true
		}
	}

	return nil, false
}

// addrsHaveIP reports whether ip is assigned among addrs.
func addrsHaveIP(addrs []net.Addr, ip net.IP) bool {
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.Equal(ip) {
			return true
		}
	}

	return false
}
