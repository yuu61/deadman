package prober

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"slices"
	"syscall"

	"golang.org/x/net/icmp"
	"golang.org/x/sys/unix"
)

func (p *icmpPinger) dial(family icmpFamily, socketType, id int) (icmpConn, error) {
	conn, err := dialICMP(family, socketType, p.source, id)
	if err != nil {
		return icmpConn{}, err
	}

	if p.echo.DF {
		err = setLinuxDF(conn.rc, family)
		if err != nil {
			discard(conn.f.Close())

			return icmpConn{}, err
		}
	}

	return conn, nil
}

func setLinuxDF(conn syscall.RawConn, family icmpFamily) error {
	level, option, value := unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_DO
	recvError := unix.IPV6_RECVERR

	if family.domain == unix.AF_INET {
		level, option, value = unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO
		recvError = unix.IP_RECVERR
	}

	var optionErr error

	err := conn.Control(func(fd uintptr) {
		optionErr = unix.SetsockoptInt(int(fd), level, option, value)
		if optionErr == nil {
			optionErr = unix.SetsockoptInt(int(fd), level, recvError, 1)
		}
	})
	if err != nil {
		return err
	}

	if optionErr != nil {
		return fmt.Errorf("set ICMP do-not-fragment: %w", optionErr)
	}

	return nil
}

// RECVERR wakes the normal reader for all queued errors, including unrelated
// unreachable errors on raw sockets and non-MTU errors (such as Host Unreachable)
// for our own probe. Ignore every successfully drained error queue unless it
// contains an MTU error (EMSGSIZE) for this probe.
// Only df needs this: without RECVERR, an unconnected socket gets no ICMP error at all
// (raw_err, rawv6_err and ping_err return early), and dialICMP never connects.
func (pr icmpProbe) skipNonMTUError(conn syscall.RawConn, peer net.IP, recvErr error) bool {
	if !pr.df || recvErr == nil {
		return false
	}

	matched, err := pr.receiveQueuedError(conn, peer)

	return !matched && err == nil
}

// errQueueBufSize holds the original datagram an ICMP error quotes: a router quotes at most
// 576 bytes in all for IPv4 and 1280 for IPv6. A longer quote is truncated, and the token
// match accepts a prefix.
const errQueueBufSize = 2048

// Linux queues the offending request separately from normal ICMP input. This works
// for ping datagram sockets and bypasses the raw socket's echo-only receive filter.
// It drains every queued error, so one for another probe ahead of ours cannot hide ours,
// and an empty queue on a concurrent error wakeup still clears sk_err.
func (pr icmpProbe) receiveQueuedError(conn syscall.RawConn, peer net.IP) (bool, error) {
	buffer := make([]byte, errQueueBufSize)
	oob := make([]byte, controlBufSize)

	var (
		matched bool
		recvErr error
	)

	err := conn.Control(func(fd uintptr) {
		for {
			n, oobn, _, dest, readErr := unix.Recvmsg(
				int(fd),
				buffer,
				oob,
				unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT,
			)
			if readErr != nil {
				if errors.Is(readErr, unix.EINTR) {
					continue
				}

				recvErr = readErr

				return
			}

			if pr.matchQueuedError(buffer[:n], oob[:oobn], dest, peer) {
				matched = true
			}
		}
	})
	if err != nil {
		return false, fmt.Errorf("read ICMP error queue: %w", err)
	}

	// Drain until the queue is empty (EAGAIN/EWOULDBLOCK). Even if the queue was
	// already empty on the first read (because the kernel reported sk_err without
	// queuing or after another read cleared it), an ICMP error wakeup that is not
	// our EMSGSIZE must not terminate the probe early as Unavailable.
	if recvErr != nil && !errors.Is(recvErr, unix.EAGAIN) && !errors.Is(recvErr, unix.EWOULDBLOCK) {
		return false, fmt.Errorf("receive ICMP error: %w", recvErr)
	}

	return matched, nil
}

func (pr icmpProbe) matchQueuedError(data, oob []byte, dest unix.Sockaddr, peer net.IP) bool {
	if !sockaddrIP(dest).Equal(peer) || !pr.matchQuotedRequest(data) {
		return false
	}

	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(messages, pr.fam.extendedError)
}

func (pr icmpProbe) matchQuotedRequest(data []byte) bool {
	request, err := icmp.ParseMessage(pr.fam.proto, data)
	if err != nil || request.Type != pr.fam.echoType || request.Code != 0 {
		return false
	}

	echo, ok := request.Body.(*icmp.Echo)

	return ok && pr.matchQuotedEcho(echo)
}

func (pr icmpProbe) matchQuotedEcho(echo *icmp.Echo) bool {
	// Routers may quote only the request header. Any quoted token prefix must agree.
	quoted := echo.Data

	return (!pr.privileged || echo.ID == pr.id) && echo.Seq == pr.seq &&
		len(quoted) <= len(pr.token) && bytes.Equal(quoted, pr.token[:len(quoted)])
}

func (f icmpFamily) extendedError(message unix.SocketControlMessage) bool {
	level, option := unix.IPPROTO_IPV6, unix.IPV6_RECVERR
	origin := uint8(unix.SO_EE_ORIGIN_ICMP6)

	if f.domain == unix.AF_INET {
		level, option = unix.IPPROTO_IP, unix.IP_RECVERR
		origin = unix.SO_EE_ORIGIN_ICMP
	}

	if message.Header.Level != int32(level) || message.Header.Type != int32(option) {
		return false
	}

	var extended unix.SockExtendedErr

	// Only a datagram too big for the path is the "size does not fit" this option measures.
	// Frag needed (IPv4) and Packet too big (IPv6) both arrive as EMSGSIZE. Another error
	// for the echo, such as an unreachable host, is a target that did not answer, as it is
	// without df.
	err := binary.Read(bytes.NewReader(message.Data), binary.NativeEndian, &extended)
	if err != nil || extended.Errno != uint32(unix.EMSGSIZE) {
		return false
	}

	return extended.Origin == origin || extended.Origin == unix.SO_EE_ORIGIN_LOCAL
}
