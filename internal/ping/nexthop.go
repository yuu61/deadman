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

// familyFor returns the forcing implementation for dst's address family. dst was
// already resolved to the next-hop's family (see resolveToFamily), so it is always
// IPv4 or IPv6 and this never returns nil.
func familyFor(dst net.IP) echoFamily {
	if dst.To4() != nil {
		return echoIPv4{}
	}

	return echoIPv6{}
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
	srcScope  v6Scope // target scope srcIP was selected for; re-select when it changes.
	cachedMAC net.HardwareAddr
}

// v6Scope is the routing scope of an IPv6 address. The forced source must share the
// target's scope (see selectSrcV6), so when a name target's resolved scope changes
// (e.g. its AAAA moves from global to ULA) the cached source must be re-selected.
type v6Scope int

const (
	scopeNone      v6Scope = iota // IPv4, or not yet selected.
	scopeLinkLocal                // fe80::/10.
	scopeULA                      // fc00::/7.
	scopeGlobal                   // global unicast.
)

// dstScope classifies dst's routing scope. IPv4 is scopeNone, which never triggers
// the IPv6 source re-selection.
func dstScope(dst net.IP) v6Scope {
	switch {
	case dst.To4() != nil:
		return scopeNone
	case dst.IsLinkLocalUnicast():
		return scopeLinkLocal
	case dst.IsPrivate():
		return scopeULA
	default:
		return scopeGlobal
	}
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

	// Forcing is same-family only: IPv4 uses ARP, IPv6 uses NDP, and their egress and
	// source selection diverge. Resolve the target to the next-hop's family; a target
	// with no address in that family (e.g. an IPv6-only name behind an IPv4 gateway)
	// cannot be force-routed and fails as X.
	dst := resolveToFamily(ctx, p.addr, p.nexthopIP)
	if dst == nil {
		return Result{Code: Failed, TTL: -1}
	}

	return p.sendForced(ctx, familyFor(dst), dst)
}

// sendForced runs the resolve→listen→build→send→wait sequence for an IPv4 target.
func (p *nexthopPinger) sendForced(ctx context.Context, fam echoFamily, dst net.IP) Result {
	r, err := p.resolve(dst) //nolint:contextcheck // local-only selection; no ctx to thread.
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

// resolve returns where to send and as what, caching the result across rounds. dst
// is the probe target, used by IPv6 egress selection to choose a scope-matched
// source. The lock serializes the (rare) overlapping probes of one target.
func (p *nexthopPinger) resolve(dst net.IP) (resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.transport == nil {
		return resolved{}, errNoTransport
	}

	if p.nexthopIP == nil {
		return resolved{}, errBadGateway
	}

	scope := dstScope(dst)

	// A name target's resolved scope can change between rounds (its AAAA moving from
	// global to ULA/link-local); the source picked for the old scope no longer routes,
	// so drop the cached selection and re-pick. Keyed on the scope the source was
	// selected for, not on the current source, so a best-effort mismatch does not
	// thrash net.Interfaces() every round.
	if p.egress != nil && scope != scopeNone && scope != p.srcScope {
		p.egress, p.srcIP, p.cachedMAC = nil, nil, nil
	}

	if p.egress == nil {
		ifi, src, err := selectEgress(p.nexthopIP, p.source, dst)
		if err != nil {
			return resolved{}, err
		}

		p.egress, p.srcIP, p.srcScope = ifi, src, scope
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

// resolveToFamily parses or resolves addr to an address in the next-hop gateway's
// family, or nil when addr has no such address. Forcing pins the family because
// ARP/IPv4 and NDP/IPv6 cannot be mixed: a name that resolves only to the other
// family is reported unreachable rather than probed via a path the next-hop cannot
// serve.
func resolveToFamily(ctx context.Context, addr string, gateway net.IP) net.IP {
	v6 := gateway.To4() == nil

	if ip := net.ParseIP(addr); ip != nil {
		if (ip.To4() == nil) == v6 {
			return ip
		}

		return nil
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, addr)
	if err != nil {
		return nil
	}

	for _, ip := range ips {
		if (ip.IP.To4() == nil) == v6 {
			return ip.IP
		}
	}

	return nil
}

// selectEgress picks the egress interface and source IP for reaching nexthop. The
// gateway must be on-link (directly connected). The IPv4 and IPv6 paths diverge
// enough (subnet match vs link scope, source selection) to warrant per-family
// helpers; dst is the probe target, consulted only by the IPv6 source choice.
func selectEgress(nexthop net.IP, source string, dst net.IP) (*net.Interface, net.IP, error) {
	if nexthop.To4() != nil {
		return selectEgressV4(nexthop, source)
	}

	return selectEgressV6(nexthop, source, dst)
}

// selectEgressV4 resolves the egress for an IPv4 next-hop. source may be empty
// (auto-select), an interface name, or a source IP; an off-link gateway or a source
// not on the egress interface is rejected so it fails at setup rather than silently
// dropping replies.
func selectEgressV4(nexthop net.IP, source string) (*net.Interface, net.IP, error) {
	if source != "" && net.ParseIP(source) == nil {
		return egressByName(source, nexthop)
	}

	return egressBySubnet(net.ParseIP(source), nexthop)
}

// selectEgressV6 resolves the egress for an IPv6 next-hop. The gateway only fixes the
// egress interface (the L2 hop); the source address must be one the target can reply
// to, so it is chosen by the target's scope, not the gateway's subnet — a global
// target needs a global source even behind a link-local gateway.
//
// A link-local gateway is on-link on every interface, so it is ambiguous without one:
// source must name the interface (source=IFNAME), and then cannot also pin a source
// IP — the address is auto-selected by scope. A global gateway is located by its
// on-link prefix, like IPv4.
func selectEgressV6(nexthop net.IP, source string, dst net.IP) (*net.Interface, net.IP, error) {
	if nexthop.IsLinkLocalUnicast() {
		if source == "" || net.ParseIP(source) != nil {
			return nil, nil, fmt.Errorf(
				"nexthop: link-local gateway %s requires source=IFNAME to fix the egress interface",
				nexthop,
			)
		}

		return egressV6Named(source, dst)
	}

	if source != "" && net.ParseIP(source) == nil {
		return egressV6Named(source, dst)
	}

	return egressV6BySubnet(net.ParseIP(source), dst, nexthop)
}

// egressV6Named resolves the egress from an explicit interface name and picks a
// scope-matched source on it.
func egressV6Named(name string, dst net.IP) (*net.Interface, net.IP, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, nil, fmt.Errorf("nexthop: interface %q: %w", name, err)
	}

	src, ok := pickSrcV6(ifi, dst)
	if !ok {
		return nil, nil, fmt.Errorf("nexthop: %s has no IPv6 source matching target %s", name, dst)
	}

	return ifi, src, nil
}

// egressV6BySubnet finds the interface whose connected IPv6 prefix contains nexthop
// and picks a scope-matched source on it. wantSrc, when set, pins the source IP
// (which must be assigned to that interface) instead of auto-selecting by scope.
func egressV6BySubnet(wantSrc, dst, nexthop net.IP) (*net.Interface, net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}

	for i := range ifaces {
		if src, ok := egressV6Candidate(&ifaces[i], wantSrc, dst, nexthop); ok {
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

// egressV6Candidate reports whether ifi can reach nexthop on-link via IPv6 and, if
// so, the source to use: wantSrc when pinned (and assigned to ifi), else a
// scope-matched address for dst.
func egressV6Candidate(ifi *net.Interface, wantSrc, dst, nexthop net.IP) (net.IP, bool) {
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 ||
		!ifaceOnLinkV6(ifi, nexthop) {
		return nil, false
	}

	if wantSrc != nil {
		addrs, _ := ifi.Addrs()

		return wantSrc, addrsHaveIP(addrs, wantSrc)
	}

	return pickSrcV6(ifi, dst)
}

// ifaceOnLinkV6 reports whether ifi has a connected IPv6 prefix that contains nexthop.
func ifaceOnLinkV6(ifi *net.Interface, nexthop net.IP) bool {
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}

	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() == nil && ipnet.Contains(nexthop) {
			return true
		}
	}

	return false
}

// pickSrcV6 returns the IPv6 source to use on ifi for reaching target. It prefers the
// address the kernel itself would choose (its RFC 6724 selection — which respects
// preferred-vs-deprecated and temporary addresses, unlike a manual scan), but only
// when that address is on ifi; a forced cross-interface target can make the kernel
// pick another interface, so it then falls back to a scope-matched address on ifi.
func pickSrcV6(ifi *net.Interface, target net.IP) (net.IP, bool) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, false
	}

	if src := kernelPreferredSrc(target); src != nil && addrsHaveIP(addrs, src) {
		return src, true
	}

	return selectSrcV6(addrs, target)
}

// kernelPreferredSrc returns the source address the kernel would use to reach target,
// or nil when it cannot be determined. It reads the kernel's choice the way ping6
// does — connect a UDP socket to the target and read back the local address — which
// sends no packet but triggers the kernel's source-address selection. Link-local
// targets are skipped (their connect would need a zone; selectSrcV6 handles them).
func kernelPreferredSrc(target net.IP) net.IP {
	if target.IsLinkLocalUnicast() {
		return nil
	}

	var d net.Dialer

	c, err := d.DialContext(context.Background(), "udp6", net.JoinHostPort(target.String(), "9"))
	if err != nil {
		return nil
	}

	defer func() { _ = c.Close() }()

	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP.IsUnspecified() {
		return nil
	}

	return ua.IP
}

// selectSrcV6 picks the IPv6 address among addrs whose scope matches target, because
// the reply returns by ordinary routing and a mismatched-scope source has no return
// path: a link-local source for a link-local target, a ULA source for a ULA target,
// and a global-unicast source for a global target (ULA and GUA are distinct — a GUA
// host cannot route back to a ULA source, and vice versa). When no same-scope source
// exists it falls back to any global source as a best effort (source= can override).
func selectSrcV6(addrs []net.Addr, target net.IP) (net.IP, bool) {
	var fallback net.IP

	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.To4() != nil {
			continue
		}

		ip := ipnet.IP
		if srcScopeMatches(ip, target) {
			return ip, true
		}

		// Best-effort fallback for a global target with no same-scope source.
		if fallback == nil && ip.IsGlobalUnicast() && !target.IsLinkLocalUnicast() {
			fallback = ip
		}
	}

	return fallback, fallback != nil
}

// srcScopeMatches reports whether source ip is in the same routing scope as target
// (link-local, ULA, or global unicast), so the target's reply to ip can return.
func srcScopeMatches(ip, target net.IP) bool {
	switch {
	case target.IsLinkLocalUnicast():
		return ip.IsLinkLocalUnicast()
	case target.IsPrivate(): // a ULA target needs a ULA source.
		return ip.IsGlobalUnicast() && ip.IsPrivate()
	default: // a global-unicast target needs a non-ULA global source.
		return ip.IsGlobalUnicast() && !ip.IsPrivate()
	}
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
