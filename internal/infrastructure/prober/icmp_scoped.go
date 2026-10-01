package prober

import (
	"bytes"
	"context"
	"errors"
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

	id, token := nextProbeID(), newProbeToken()
	message := icmp.Message{
		Type: ipv6.ICMPTypeEchoRequest,
		Body: &icmp.Echo{ID: id, Seq: 1, Data: token},
	}

	data, err := message.Marshal(nil)
	if err != nil || ctx.Err() != nil {
		return probe.UnavailableResult()
	}

	peer := *dest
	if peer.Zone == "" && peer.IP.IsLinkLocalUnicast() {
		peer.Zone = source.Zone()
	}

	var target net.Addr = &peer
	if !p.privileged {
		target = &net.UDPAddr{IP: peer.IP, Zone: peer.Zone}
	}

	start := time.Now()

	_, err = conn.WriteTo(data, target)
	if err != nil {
		return probe.UnavailableResult()
	}

	return p.receiveScoped(ctx, conn, peer.IP, id, token, start)
}

func (p *icmpPinger) receiveScoped(
	ctx context.Context, conn *icmp.PacketConn, peer net.IP, id int, token []byte, start time.Time,
) probe.Result {
	buffer := make([]byte, recvBufSize)
	for {
		n, from, err := conn.ReadFrom(buffer)
		if err != nil {
			if isTimeout(err) && !errors.Is(ctx.Err(), context.Canceled) {
				return probe.FailedResult()
			}

			return probe.UnavailableResult()
		}

		if scopedReply(buffer[:n], from, peer, id, token, p.privileged) {
			return probe.SuccessResult(float64(time.Since(start).Microseconds()) / usPerMs)
		}
	}
}

func scopedReply(
	data []byte,
	from net.Addr,
	peer net.IP,
	id int,
	token []byte,
	privileged bool,
) bool {
	if !addrIP(from).Equal(peer) {
		return false
	}

	message, err := icmp.ParseMessage(ianaProtocolICMPv6, data)
	if err != nil || message.Type != ipv6.ICMPTypeEchoReply {
		return false
	}

	echo, ok := message.Body.(*icmp.Echo)

	return ok && (!privileged || echo.ID == id) && echo.Seq == 1 && bytes.Equal(echo.Data, token)
}
