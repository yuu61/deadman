package prober

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const (
	ipv4IHLMask     = 0x0f
	ipv4IHLWordSize = 4
)

// nativeEchoConn retains the underlying socket so buffers and DF can be set before
// it is wrapped by x/net. Its IPv4 reader also handles Darwin's IP_STRIPHDR behavior.
type nativeEchoConn struct {
	net.PacketConn

	v4      *ipv4.PacketConn
	v6      *ipv6.PacketConn
	ifIndex int
}

func (c *nativeEchoConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	var (
		n    int
		from net.Addr
		err  error
	)

	if c.v4 != nil {
		n, _, from, err = c.v4.ReadFrom(buffer)
	} else {
		n, _, from, err = c.v6.ReadFrom(buffer)
	}

	if err != nil {
		return n, from, fmt.Errorf("read native ICMP echo: %w", err)
	}

	if c.v4 != nil {
		data := stripNativeIPv4Header(buffer[:n])
		n = copy(buffer, data)
	}

	return n, from, nil
}

// Darwin's ICMP datagram socket retains the IPv4 header even through x/net/ipv4.
// Other readers already remove it, so strip only when the version and IHL agree.
func stripNativeIPv4Header(data []byte) []byte {
	if len(data) < ipv4.HeaderLen || data[0]>>4 != ipv4.Version {
		return data
	}

	headerLen := int(data[0]&ipv4IHLMask) * ipv4IHLWordSize
	if headerLen < ipv4.HeaderLen || headerLen > len(data) {
		return data
	}

	return data[headerLen:]
}

func (c *nativeEchoConn) WriteTo(data []byte, dest net.Addr) (int, error) {
	var (
		n   int
		err error
	)

	if c.v4 != nil {
		n, err = c.v4.WriteTo(data, &ipv4.ControlMessage{IfIndex: c.ifIndex}, dest)
	} else {
		n, err = c.v6.WriteTo(data, &ipv6.ControlMessage{IfIndex: c.ifIndex}, dest)
	}

	if err != nil {
		return n, fmt.Errorf("write native ICMP echo: %w", err)
	}

	return n, nil
}

// sendNativeEcho sends probes through sockets with payload-sized buffers and, when asked,
// the do-not-fragment bit. Linux uses its kernel-timestamped Send.
func (p *icmpPinger) sendNativeEcho(ctx context.Context, dest *net.IPAddr) probe.Result {
	conn, err := p.listenNativeEcho(dest)
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
	kind := echoKindV6

	var echoType icmp.Type = ipv6.ICMPTypeEchoRequest

	if dest.IP.To4() != nil {
		kind = echoKindV4
		echoType = ipv4.ICMPTypeEcho
	}

	message := icmp.Message{Type: echoType, Body: &icmp.Echo{ID: id, Seq: 1, Data: payload}}

	data, err := message.Marshal(nil)
	if err != nil || ctx.Err() != nil {
		return probe.UnavailableResult()
	}

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

	return p.receiveNativeEcho(ctx, conn, peer.IP, id, payload, start, kind)
}

func (p *icmpPinger) nativeEchoPeer(dest *net.IPAddr) net.IPAddr {
	peer := *dest
	if peer.Zone == "" && peer.IP.IsLinkLocalUnicast() {
		if source, ok := p.source.Addr(); ok {
			peer.Zone = source.Zone()
		} else if name, hasName := p.source.Interface(); hasName {
			peer.Zone = name
		}
	}

	return peer
}

func (p *icmpPinger) receiveNativeEcho(
	ctx context.Context,
	conn net.PacketConn,
	peer net.IP,
	id int,
	payload []byte,
	start time.Time,
	kind echoReplyKind,
) probe.Result {
	buffer := make([]byte, echoReceiveSize(len(payload)))
	for {
		n, from, err := conn.ReadFrom(buffer)
		if err != nil {
			if isTimeout(err) && !errors.Is(ctx.Err(), context.Canceled) {
				return probe.FailedResult()
			}

			return probe.UnavailableResult()
		}

		if nativeEchoReply(buffer[:n], from, peer, id, payload, p.privileged, kind) {
			return probe.SuccessResult(float64(time.Since(start).Microseconds()) / usPerMs)
		}
	}
}

func nativeEchoReply(
	data []byte,
	from net.Addr,
	peer net.IP,
	id int,
	payload []byte,
	privileged bool,
	kind echoReplyKind,
) bool {
	if !addrIP(from).Equal(peer) {
		return false
	}

	message, err := icmp.ParseMessage(kind.proto, data)
	if err != nil || message.Type != kind.replyType {
		return false
	}

	echo, ok := message.Body.(*icmp.Echo)

	return ok && (!privileged || echo.ID == id) && echo.Seq == 1 &&
		bytes.Equal(echo.Data, payload)
}
