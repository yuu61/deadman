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
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
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

// An EMSGSIZE wakes the normal reader before its quoted request is read from the
// error queue. Ignore it only when the queued error belongs to another probe.
func (pr icmpProbe) skipForeignMTUError(conn syscall.RawConn, peer net.IP, recvErr error) bool {
	if !pr.df || !errors.Is(recvErr, unix.EMSGSIZE) {
		return false
	}

	matched, err := pr.receiveMTUError(conn, peer)

	return !matched && err == nil
}

// Linux queues the offending request separately from normal ICMP input. This works
// for ping datagram sockets and bypasses the raw socket's echo-only receive filter.
func (pr icmpProbe) receiveMTUError(conn syscall.RawConn, peer net.IP) (bool, error) {
	buffer := make([]byte, echoReceiveSize(len(pr.token)))
	oob := make([]byte, controlBufSize)

	var (
		matched bool
		recvErr error
	)

	err := conn.Control(func(fd uintptr) {
		n, oobn, _, dest, readErr := unix.Recvmsg(
			int(fd),
			buffer,
			oob,
			unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT,
		)
		recvErr = readErr

		if readErr == nil {
			matched = pr.matchMTUError(buffer[:n], oob[:oobn], dest, peer)
		}
	})
	if err != nil {
		return false, fmt.Errorf("read ICMP error queue: %w", err)
	}

	if recvErr != nil && !retryable(recvErr) {
		return false, fmt.Errorf("receive ICMP error: %w", recvErr)
	}

	return matched, nil
}

func (pr icmpProbe) matchMTUError(data, oob []byte, dest unix.Sockaddr, peer net.IP) bool {
	if !sockaddrIP(dest).Equal(peer) || !pr.matchQuotedRequest(data) {
		return false
	}

	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(messages, pr.fam.mtuError)
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

func (f icmpFamily) mtuError(message unix.SocketControlMessage) bool {
	level, option := unix.IPPROTO_IPV6, unix.IPV6_RECVERR
	origin, kind, code := uint8(unix.SO_EE_ORIGIN_ICMP6), uint8(ipv6.ICMPTypePacketTooBig), uint8(0)

	if f.domain == unix.AF_INET {
		level, option = unix.IPPROTO_IP, unix.IP_RECVERR
		origin, kind, code = unix.SO_EE_ORIGIN_ICMP, uint8(ipv4.ICMPTypeDestinationUnreachable), 4
	}

	if message.Header.Level != int32(level) || message.Header.Type != int32(option) {
		return false
	}

	var extended unix.SockExtendedErr

	err := binary.Read(bytes.NewReader(message.Data), binary.NativeEndian, &extended)
	if err != nil || !errors.Is(unix.Errno(extended.Errno), unix.EMSGSIZE) {
		return false
	}

	return extended.Origin == origin && extended.Type == kind && extended.Code == code
}
