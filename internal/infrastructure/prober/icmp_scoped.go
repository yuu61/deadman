package prober

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// resolvePortable keeps DNS within the probe's lifetime and preserves literal zones.
func (p *icmpPinger) resolvePortable(ctx context.Context) (*net.IPAddr, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}

	if a, ok := p.dest.IP(); ok {
		return &net.IPAddr{IP: net.IP(a.AsSlice()), Zone: a.Zone()}, nil
	}

	network := p.network
	if network == "" {
		network = networkAny
	}

	name, _ := p.dest.Name()

	addresses, err := net.DefaultResolver.LookupIP(ctx, network, name)
	if err != nil {
		return nil, err
	}

	if len(addresses) == 0 {
		return nil, fmt.Errorf("ping: no addresses for %s", name)
	}

	// Match net.ResolveIPAddr's IPv4 preference for a name in automatic mode.
	chosen := addresses[0]
	if network == networkAny {
		for _, address := range addresses {
			if address.To4() != nil {
				chosen = address

				break
			}
		}
	}

	return &net.IPAddr{IP: chosen}, nil
}

// sendScoped binds the full scoped source. pro-bing's Source validation uses ParseIP,
// which cannot accept zones, so this path sends and matches one IPv6 echo directly.
func (p *icmpPinger) sendScoped(
	ctx context.Context,
	dest *net.IPAddr,
	source netip.Addr,
) probe.Result {
	network := "udp6"
	if p.privileged {
		network = "ip6:ipv6-icmp"
	}

	conn, err := icmp.ListenPacket(network, source.String())
	if err != nil {
		return probe.UnavailableResult()
	}
	defer conn.Close()

	err = conn.SetDeadline(probeDeadline(ctx))
	if err != nil {
		return probe.UnavailableResult()
	}

	stop := interruptOnDone(ctx, conn.SetDeadline)
	defer stop()

	id, payload := nextProbeID(), echoPayload(p.echo.Size.Bytes())
	message := icmp.Message{
		Type: ipv6.ICMPTypeEchoRequest,
		Body: &icmp.Echo{ID: id, Seq: 1, Data: payload},
	}

	data, err := message.Marshal(nil)
	if err != nil || ctx.Err() != nil {
		return probe.UnavailableResult()
	}

	// The source is this row's, so a link-local destination takes its zone.
	peer := p.nativeEchoPeer(dest)

	var target net.Addr = &peer
	if !p.privileged {
		target = &net.UDPAddr{IP: peer.IP, Zone: peer.Zone}
	}

	start := time.Now()

	_, err = conn.WriteTo(data, target)
	if err != nil {
		return probe.UnavailableResult()
	}

	return p.receiveNativeEcho(ctx, conn, peer.IP, id, payload, start, echoKindV6)
}
