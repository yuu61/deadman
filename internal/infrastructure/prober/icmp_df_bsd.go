//go:build darwin || freebsd

package prober

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func (p *icmpPinger) listenDF(dest *net.IPAddr) (*nativeEchoConn, error) {
	fd, err := p.dfSocket(dest)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), "icmp-df")
	defer func() { discard(file.Close()) }()

	conn, err := net.FilePacketConn(file)
	if err != nil {
		return nil, err
	}

	index, err := dfInterface(p.source)
	if err != nil {
		discard(conn.Close())

		return nil, err
	}

	native := &nativeEchoConn{PacketConn: conn, ifIndex: index}
	if dest.IP.To4() != nil {
		native.v4 = ipv4.NewPacketConn(conn)
	} else {
		native.v6 = ipv6.NewPacketConn(conn)
	}

	return native, nil
}

func dfInterface(source probe.Source) (int, error) {
	name, ok := source.Interface()
	if !ok {
		return 0, nil
	}

	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("DF source interface: %w", err)
	}

	return iface.Index, nil
}

func (p *icmpPinger) dfSocket(dest *net.IPAddr) (int, error) {
	family, protocol := unix.AF_INET6, unix.IPPROTO_ICMPV6
	level, option := unix.IPPROTO_IPV6, unix.IPV6_DONTFRAG

	if dest.IP.To4() != nil {
		family, protocol = unix.AF_INET, unix.IPPROTO_ICMP
		level, option = unix.IPPROTO_IP, unix.IP_DONTFRAG
	}

	socketType := unix.SOCK_DGRAM
	if p.privileged {
		socketType = unix.SOCK_RAW
	}

	fd, err := unix.Socket(family, socketType, protocol)
	if err != nil {
		return -1, fmt.Errorf("open DF ICMP socket: %w", err)
	}

	unix.CloseOnExec(fd)

	err = growDFSocketBuffers(fd, p.echo.Size.Bytes())
	if err == nil {
		err = unix.SetsockoptInt(fd, level, option, 1)
	}

	if err == nil {
		err = p.bindDFSource(fd)
	}

	if err != nil {
		discard(unix.Close(fd))

		return -1, fmt.Errorf("configure DF ICMP socket: %w", err)
	}

	return fd, nil
}

// BSD raw sockets may default to 8192 bytes, too small for a jumbo echo. Grow
// both kernel buffers before sending, retaining any larger system defaults.
func growDFSocketBuffers(fd, payload int) error {
	size := echoSocketBufferSize(payload)

	for _, option := range []int{unix.SO_SNDBUF, unix.SO_RCVBUF} {
		current, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, option)
		if err != nil {
			return fmt.Errorf("get ICMP socket buffer: %w", err)
		}

		if current >= size {
			continue
		}

		err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, option, size)
		if err != nil {
			return fmt.Errorf("grow ICMP socket buffer: %w", err)
		}
	}

	return nil
}

func (p *icmpPinger) bindDFSource(fd int) error {
	source, ok := p.source.Addr()
	if !ok {
		return nil
	}

	var address unix.Sockaddr
	if source.Is4() {
		address = &unix.SockaddrInet4{Addr: source.As4()}
	} else {
		zone, err := dfZone(source.Zone())
		if err != nil {
			return err
		}

		address = &unix.SockaddrInet6{Addr: source.As16(), ZoneId: zone}
	}

	err := unix.Bind(fd, address)
	if err != nil {
		return fmt.Errorf("bind DF source: %w", err)
	}

	return nil
}

func dfZone(name string) (uint32, error) {
	if name == "" {
		return 0, nil
	}

	number, err := strconv.ParseUint(name, 10, 32)
	if err == nil {
		return uint32(number), nil
	}

	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, err
	}

	if iface.Index < 0 || iface.Index > math.MaxInt32 {
		return 0, fmt.Errorf("invalid DF interface index %d", iface.Index)
	}

	return uint32(iface.Index), nil
}
