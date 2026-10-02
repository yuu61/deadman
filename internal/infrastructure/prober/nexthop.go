package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// The next-hop pinger forces a direct ICMP probe out through a chosen gateway
// (next-hop) instead of letting the kernel pick the route. Neither pro-bing nor a
// plain IP raw socket can override the next-hop (the kernel routes by destination
// IP), and mutating the routing table races with concurrent probes. The only
// clean, stateless way is to send the frame at L2 addressed to the gateway's MAC
// (see linkTransport); the reply returns by ordinary routing and is read back on a
// raw ICMP socket (see echoFamily).
//
// The address-family work sits behind one seam, echoFamily: build the ICMP echo,
// open the raw listener, match the reply. Both families are implemented — IPv4 over
// ICMP/ARP (echoIPv4) and IPv6 over ICMPv6/NDP (echoIPv6) — and slot in behind it
// without touching the transport or the orchestration core.
//
// The OS-specific half — putting an L3 packet on the wire toward a MAC and resolving
// that MAC from the kernel neighbor cache — is linkTransport (Linux AF_PACKET).
// Next-hop forcing is a Linux-only feature, like the other Linux-bound methods
// (netns/vrf through `ip`): the AF_PACKET injection it needs has no
// portable equivalent, so the transport is build-tagged, and elsewhere a forced target
// is not built: its build error says why. It is not a portability roadmap.
//
// Dispatch, egress selection, caching and the resolve→build→send→recv orchestration
// are family-agnostic and live here. Shared ICMP IDs, tokens and deadlines live in icmp.go.

// errNoTransport is the build error of a forced target where forcing is impossible.
var errNoTransport = errors.New("next-hop forcing is only supported on Linux")

// errGatewayUnresolved is a gateway whose link-layer address the neighbor cache never
// learned: it did not answer ARP or NDP, so the forced path is down at its first hop.
var errGatewayUnresolved = errors.New("nexthop: gateway did not answer neighbor resolution")

// errNexthopTimeout distinguishes the probe's own deadline from caller cancellation.
var errNexthopTimeout = errors.New("nexthop: probe deadline exceeded")

// linkTransport is the OS-specific half of next-hop forcing. The Linux AF_PACKET
// implementation lives in nexthop_transport_linux.go; nexthop_transport_other.go has
// none, so newLinkTransport fails there. Receiving replies is cross-platform, so it is
// not here.
type linkTransport interface {
	// resolveGateway returns the link-layer address of nexthop as reached out
	// iface, consulting (and if needed populating) the OS neighbor cache. A gateway that
	// never answers is errGatewayUnresolved.
	resolveGateway(
		ctx context.Context,
		iface *net.Interface,
		nexthop net.IP,
	) (net.HardwareAddr, error)
	// send transmits an already-built L3 packet to dstMAC out iface. ethertype
	// selects the L3 protocol carried in the frame.
	send(iface *net.Interface, dstMAC net.HardwareAddr, ethertype uint16, l3 []byte) error
}

// echoFamily is the address-family-specific half: building the echo request,
// opening a raw ICMP listener, and matching the reply. Both families are
// implemented: echoIPv4 (ICMP/ARP) and echoIPv6 (ICMPv6/NDP).
type echoFamily interface {
	ethertype() uint16
	build(src, dst net.IP, id, seq int, token []byte) ([]byte, error)
	listen(src net.IP, id int) (replyWaiter, error)
}

// replyWaiter blocks for the echo reply to one probe. close releases the listener.
type replyWaiter interface {
	// wait reports ok once an echo reply matching id/seq/token from peer arrives, or
	// ok=false once deadline passes with no match.
	wait(
		ctx context.Context,
		peer net.IP,
		id, seq int,
		token []byte,
		deadline time.Time,
	) (ok bool, err error)
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

// nexthopPinger forces a direct ICMP probe to addr via the gateway nexthopIP. It
// is built only for an explicit Nexthop plan.
type nexthopPinger struct {
	dest      probe.Destination
	nexthopIP net.IP
	family    probe.Family // the gateway's, which the probe is forced in.
	source    probe.Source
	transport linkTransport

	mu        sync.Mutex
	egress    *net.Interface
	srcIP     net.IP
	srcScope  v6Scope // target scope srcIP was selected for; re-select when it changes.
	cachedMAC net.HardwareAddr
}

// v6Scope records the destination scope and ULA/GUA source preference. A changed
// destination should reconsider that preference even though ULA and GUA share global
// scope and can use one another as a fallback.
type v6Scope int

const (
	scopeNone      v6Scope = iota // IPv4, or not yet selected.
	scopeLinkLocal                // fe80::/10.
	scopeULA                      // fc00::/7.
	scopeGlobal                   // global unicast.
)

// dstScope classifies dst's scope and source preference. IPv4 is scopeNone, which never triggers
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

// newNexthopPinger builds the forced probe of a compiled plan, whose gateway is an
// address of the target literal's family (probe.Compile rejects anything else). Where
// this platform cannot force a next hop the target is not built at all, rather than
// failing every round.
func newNexthopPinger(dest probe.Destination, n probe.Nexthop) (probe.Pinger, error) {
	transport, err := newLinkTransport()
	if err != nil {
		return nil, err
	}

	return &nexthopPinger{
		dest:      dest,
		nexthopIP: net.IP(n.Gateway.AsSlice()),
		family:    n.Family(),
		source:    n.Source,
		transport: transport,
	}, nil
}

func (p *nexthopPinger) Send(ctx context.Context) probe.Result {
	// Bound DNS, neighbor resolution and the echo to one deadline. Generation
	// cancellation also stops each phase without waiting for the probe timeout.
	ctx, cancel := context.WithTimeoutCause(ctx, icmpTimeout, errNexthopTimeout)
	defer cancel()

	// Forcing is same-family only: IPv4 uses ARP, IPv6 uses NDP, and their egress and
	// source selection diverge. The plan fixes the probe's family to the gateway's; a
	// name with no address in it (e.g. an IPv6-only name behind an IPv4 gateway) cannot
	// be force-routed, which says nothing about the target.
	dst := resolveInFamily(ctx, p.dest, p.family)
	if dst == nil {
		return probe.UnavailableResult()
	}

	return p.sendForced(ctx, familyFor(dst), dst)
}

// sendForced runs the resolve→listen→build→send→wait sequence for one target (either
// family). A gateway that does not answer neighbor resolution is the forced path failing
// at its first hop, as a plain probe through that dead gateway would time out: target
// loss. Any other failure before the echo leaves (no egress, no socket) observed nothing.
func (p *nexthopPinger) sendForced(ctx context.Context, fam echoFamily, dst net.IP) probe.Result {
	route, err := p.resolve(ctx, dst)
	if errors.Is(err, errGatewayUnresolved) {
		return probe.FailedResult()
	}

	if err != nil {
		return probe.UnavailableResult()
	}

	// Resolving the name and the gateway may have spent the probe's time: an echo sent
	// now could not be waited for, so it would count a loss nothing observed.
	deadline := probeDeadline(ctx)
	if ctx.Err() != nil || time.Until(deadline) <= 0 {
		return probe.UnavailableResult()
	}

	id, seq, token := nextProbeID(), 1, newProbeToken()

	waiter, err := fam.listen(route.src, id)
	if err != nil {
		p.reset() // the source IP may be gone (interface recreated); re-select next round.

		return probe.UnavailableResult()
	}

	defer func() { _ = waiter.close() }()

	pkt, err := fam.build(route.src, dst, id, seq, token)
	if err != nil {
		return probe.UnavailableResult()
	}

	start := time.Now()

	err = p.transport.send(route.iface, route.mac, fam.ethertype(), pkt)
	if err != nil {
		p.reset() // the ifindex/MAC may be stale (interface or gateway changed); re-select.

		return probe.UnavailableResult()
	}

	ok, err := waiter.wait(ctx, dst, id, seq, token, deadline)
	if err != nil {
		p.invalidateMAC()

		return probe.UnavailableResult()
	}

	if !ok {
		p.invalidateMAC() // no reply: the gateway MAC may have changed; re-read the neighbor table next round.

		return probe.FailedResult()
	}

	rtt := float64(time.Since(start).Microseconds()) / usPerMs

	return probe.SuccessResult(rtt)
}

// resolve returns where to send and as what, caching the result across rounds. dst
// is the probe target, used by IPv6 egress selection to choose a scope-matched
// source. The lock serializes the (rare) overlapping probes of one target.
func (p *nexthopPinger) resolve(ctx context.Context, dst net.IP) (resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	scope := dstScope(dst)

	// A name target's resolved scope can change between rounds (its AAAA moving from
	// global to ULA/link-local); reconsider the preferred source on that interface.
	// Drop the cached selection and re-pick. Keyed on the scope the source was
	// selected for, not on the current source, so a best-effort mismatch does not
	// thrash net.Interfaces() every round.
	if p.egress != nil && scope != scopeNone && scope != p.srcScope {
		p.egress, p.srcIP, p.cachedMAC = nil, nil, nil
	}

	if p.egress == nil {
		ifi, src, err := selectEgress(ctx, p.nexthopIP, p.source, dst)
		if err != nil {
			return resolved{}, err
		}

		p.egress, p.srcIP, p.srcScope = ifi, src, scope
	}

	if p.cachedMAC == nil {
		mac, err := p.transport.resolveGateway(ctx, p.egress, p.nexthopIP)
		if err != nil {
			// The interface can disappear before we reach listen/send, where the
			// other cache resets live. Re-select on the next round after lookup fails.
			p.egress, p.srcIP, p.cachedMAC = nil, nil, nil

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

// resolveInFamily returns the destination's address in the forced family, or nil when a
// name has none. An address destination is of that family already and has no zone
// (Compile refuses either, since the egress is chosen from the gateway). Forcing pins the
// family because ARP/IPv4 and NDP/IPv6 cannot be mixed: a name that resolves only to the
// other family is reported unreachable rather than probed via a path the next-hop cannot
// serve.
func resolveInFamily(ctx context.Context, dest probe.Destination, fam probe.Family) net.IP {
	if a, ok := dest.IP(); ok {
		return net.IP(a.AsSlice())
	}

	name, _ := dest.Name()

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return nil
	}

	for _, ip := range ips {
		if ipFamily(ip.IP) == fam {
			return ip.IP
		}
	}

	return nil
}

// ipFamily reports the family of a resolved address.
func ipFamily(ip net.IP) probe.Family {
	if ip.To4() != nil {
		return probe.FamilyIPv4
	}

	return probe.FamilyIPv6
}

// selectEgress picks the egress interface and source IP for reaching nexthop. The
// gateway must be on-link (directly connected). The IPv4 and IPv6 paths diverge
// enough (subnet match vs link scope, source selection) to warrant per-family
// helpers; dst is the probe target, consulted only by the IPv6 source choice.
func selectEgress(
	ctx context.Context,
	nexthop net.IP,
	source probe.Source,
	dst net.IP,
) (*net.Interface, net.IP, error) {
	if nexthop.To4() != nil {
		return selectEgressV4(nexthop, source)
	}

	return selectEgressV6(ctx, nexthop, source, dst)
}

// sourceIP is a source address as the egress selection compares it, or nil for an
// omitted source or an interface. Compile has given it the gateway's family and no zone.
func sourceIP(source probe.Source) net.IP {
	if a, ok := source.Addr(); ok {
		return net.IP(a.AsSlice())
	}

	return nil
}

// selectEgressV4 resolves the egress for an IPv4 next-hop. source may be omitted
// (auto-select), an interface, or a source address; an off-link gateway or a source
// not on the egress interface is rejected so it fails at setup rather than silently
// dropping replies.
func selectEgressV4(nexthop net.IP, source probe.Source) (*net.Interface, net.IP, error) {
	if name, ok := source.Interface(); ok {
		return egressByName(name, nexthop)
	}

	return egressBySubnet(sourceIP(source), nexthop)
}

// selectEgressV6 resolves the egress for an IPv6 next-hop. The gateway only fixes the
// egress interface (the L2 hop); the source address must be one the target can reply
// to, so it is chosen by the target's scope, not the gateway's subnet — a global
// target needs a global source even behind a link-local gateway.
//
// A link-local gateway is on-link on every interface, so it is ambiguous without one:
// Compile requires the source to name the interface (source=IFNAME), and the address is
// then auto-selected by scope. A global gateway is located by its on-link prefix, like
// IPv4.
func selectEgressV6(
	ctx context.Context,
	nexthop net.IP,
	source probe.Source,
	dst net.IP,
) (*net.Interface, net.IP, error) {
	if name, ok := source.Interface(); ok {
		return egressV6Named(ctx, name, dst)
	}

	return egressV6BySubnet(ctx, sourceIP(source), dst, nexthop)
}

// egressV6Named resolves the egress from an explicit interface name and picks a
// scope-matched source on it.
func egressV6Named(ctx context.Context, name string, dst net.IP) (*net.Interface, net.IP, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, nil, fmt.Errorf("nexthop: interface %q: %w", name, err)
	}

	src, ok := pickSrcV6(ctx, ifi, dst)
	if !ok {
		return nil, nil, fmt.Errorf("nexthop: %s has no IPv6 source matching target %s", name, dst)
	}

	return ifi, src, nil
}

// egressV6BySubnet finds the interface whose connected IPv6 prefix contains nexthop
// and picks a scope-matched source on it. wantSrc, when set, pins the source IP
// (which must be assigned to that interface) instead of auto-selecting by scope.
func egressV6BySubnet(
	ctx context.Context,
	wantSrc, dst, nexthop net.IP,
) (*net.Interface, net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}

	for i := range ifaces {
		if src, ok := egressV6Candidate(ctx, &ifaces[i], wantSrc, dst, nexthop); ok {
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
func egressV6Candidate(
	ctx context.Context,
	ifi *net.Interface,
	wantSrc, dst, nexthop net.IP,
) (net.IP, bool) {
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 ||
		!ifaceOnLinkV6(ifi, nexthop) {
		return nil, false
	}

	if wantSrc != nil {
		addrs, err := ifi.Addrs()
		if err != nil {
			// An interface whose addresses cannot be listed does not hold the source.
			return wantSrc, false
		}

		return wantSrc, addrsHaveIP(addrs, wantSrc)
	}

	return pickSrcV6(ctx, ifi, dst)
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
func pickSrcV6(ctx context.Context, ifi *net.Interface, target net.IP) (net.IP, bool) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, false
	}

	if src := kernelPreferredSrc(ctx, target); src != nil && addrsHaveIP(addrs, src) {
		return src, true
	}

	return selectSrcV6(addrs, target)
}

// kernelPreferredSrc returns the source address the kernel would use to reach target,
// or nil when it cannot be determined. It reads the kernel's choice the way ping6
// does — connect a UDP socket to the target and read back the local address — which
// sends no packet but triggers the kernel's source-address selection. Link-local
// targets are skipped (their connect would need a zone; selectSrcV6 handles them).
func kernelPreferredSrc(ctx context.Context, target net.IP) net.IP {
	if target.IsLinkLocalUnicast() {
		return nil
	}

	var d net.Dialer

	c, err := d.DialContext(ctx, "udp6", net.JoinHostPort(target.String(), "9"))
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

// selectSrcV6 prefers matching ULA/GUA labels, but both have global scope and
// can communicate when return routes exist. Link-local scope remains mandatory.
func selectSrcV6(addrs []net.Addr, target net.IP) (net.IP, bool) {
	var fallback net.IP

	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.To4() != nil {
			continue
		}

		if srcScopeMatches(ipnet.IP, target) {
			return ipnet.IP, true
		}

		if fallback == nil && !target.IsLinkLocalUnicast() && ipnet.IP.IsGlobalUnicast() {
			fallback = ipnet.IP
		}
	}

	return fallback, fallback != nil
}

// srcScopeMatches reports whether source ip matches the preferred destination label
// (link-local, ULA, or GUA); selectSrcV6 also permits a global-scope fallback.
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
