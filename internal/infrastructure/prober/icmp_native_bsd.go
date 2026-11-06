//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package prober

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const nativeBSDEcho = true

func (p *icmpPinger) listenNativeEcho(dest *net.IPAddr) (*nativeEchoConn, error) {
	fd, err := p.nativeEchoSocket(dest)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), "icmp-echo")
	defer func() { discard(file.Close()) }()

	conn, err := net.FilePacketConn(file)
	if err != nil {
		return nil, err
	}

	index, err := nativeEchoInterface(p.source)
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

func nativeEchoInterface(source probe.Source) (int, error) {
	name, ok := source.Interface()
	if !ok {
		return 0, nil
	}

	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("ICMP source interface: %w", err)
	}

	return iface.Index, nil
}

func (p *icmpPinger) nativeEchoSocket(dest *net.IPAddr) (int, error) {
	family, protocol := unix.AF_INET6, unix.IPPROTO_ICMPV6

	if dest.IP.To4() != nil {
		family, protocol = unix.AF_INET, unix.IPPROTO_ICMP
	}

	socketType := unix.SOCK_DGRAM
	if p.privileged {
		socketType = unix.SOCK_RAW
	}

	fd, err := openCloseOnExecSocket(family, socketType, protocol)
	if err != nil {
		return -1, err
	}

	err = growICMPSocketBuffers(fd, p.echo.Size.Bytes())
	if err == nil {
		err = p.bindNativeEchoSource(fd)
	}

	if err != nil {
		discard(unix.Close(fd))

		return -1, fmt.Errorf("configure ICMP socket: %w", err)
	}

	return fd, nil
}

// openCloseOnExecSocket opens a socket that a concurrently started child (an ssh or ip
// relay probe) cannot inherit. Darwin has no SOCK_CLOEXEC, so, as the net package does
// there, ForkLock keeps a fork out of the gap between socket and CloseOnExec.
func openCloseOnExecSocket(family, socketType, protocol int) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	fd, err := unix.Socket(family, socketType, protocol)
	if err != nil {
		return -1, fmt.Errorf("open ICMP socket: %w", err)
	}

	unix.CloseOnExec(fd)

	return fd, nil
}

// BSD raw sockets may default to 8192 bytes, too small for a jumbo echo. Grow
// both kernel buffers before sending, retaining any larger system defaults.
func growICMPSocketBuffers(fd, payload int) error {
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

func (p *icmpPinger) bindNativeEchoSource(fd int) error {
	source, ok := p.source.Addr()
	if !ok {
		return nil
	}

	var address unix.Sockaddr
	if source.Is4() {
		address = &unix.SockaddrInet4{Addr: source.As4()}
	} else {
		zone, err := nativeEchoZone(source.Zone())
		if err != nil {
			return err
		}

		address = &unix.SockaddrInet6{Addr: source.As16(), ZoneId: zone}
	}

	err := unix.Bind(fd, address)
	if err != nil {
		return fmt.Errorf("bind ICMP source: %w", err)
	}

	return nil
}

func nativeEchoZone(name string) (uint32, error) {
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
		return 0, fmt.Errorf("invalid ICMP interface index %d", iface.Index)
	}

	return uint32(iface.Index), nil
}
