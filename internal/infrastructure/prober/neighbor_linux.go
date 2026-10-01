//go:build linux

package prober

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const neighborReplySize = 4096

// neighLookup requests one neighbor by interface and destination, without NLM_F_DUMP.
// A kernel/cache error is distinct from ENOENT (a gateway not yet resolved).
func neighLookup(
	ctx context.Context,
	ifindex int,
	ip net.IP,
) (net.HardwareAddr, neighState, error) {
	ctx, cancel := context.WithTimeout(ctx, neighborResolveTimeout)
	defer cancel()

	err := ctx.Err()
	if err != nil {
		return nil, neighMissing, err
	}

	request, family, err := neighborRequest(ifindex, ip)
	if err != nil {
		return nil, neighMissing, err
	}

	f, err := neighborSocket(ctx)
	if err != nil {
		return nil, neighMissing, err
	}
	defer func() { discard(f.Close()) }()

	stop := interruptOnDone(ctx, f.SetDeadline)
	defer stop()

	raw, err := neighborQuery(f, request)

	if ctx.Err() != nil {
		return nil, neighMissing, ctx.Err()
	}

	if err != nil {
		return nil, neighMissing, err
	}

	return neighborReply(raw, family, ifindex, ip)
}

func neighborRequest(ifindex int, ip net.IP) ([]byte, int, error) {
	if ifindex <= 0 || ifindex > math.MaxInt32 {
		return nil, 0, fmt.Errorf("invalid neighbor interface index %d", ifindex)
	}

	family := uint8(unix.AF_INET6)
	addr := ip.To16()

	attrLen := uint16(unix.SizeofRtAttr + net.IPv6len)
	if v4 := ip.To4(); v4 != nil {
		family, addr, attrLen = unix.AF_INET, v4, unix.SizeofRtAttr+net.IPv4len
	}

	if addr == nil {
		return nil, 0, errAddrFamily
	}

	header := unix.NlMsghdr{
		Len:   uint32(unix.SizeofNlMsghdr+unix.SizeofNdMsg) + uint32(attrLen),
		Type:  unix.RTM_GETNEIGH,
		Flags: unix.NLM_F_REQUEST,
		Seq:   1,
	}
	nd := unix.NdMsg{Family: family, Ifindex: int32(ifindex)}
	attr := unix.RtAttr{Len: attrLen, Type: unix.NDA_DST}

	request := make([]byte, 0, header.Len)
	for _, value := range []any{header, nd, attr} {
		var err error

		request, err = binary.Append(request, binary.NativeEndian, value)
		if err != nil {
			return nil, 0, fmt.Errorf("encode the neighbor request: %w", err)
		}
	}

	return append(request, addr...), int(family), nil
}

func neighborSocket(ctx context.Context) (*os.File, error) {
	fd, err := unix.Socket(
		unix.AF_NETLINK,
		unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC,
		unix.NETLINK_ROUTE,
	)
	if err != nil {
		return nil, fmt.Errorf("open a netlink socket: %w", err)
	}

	f := os.NewFile(uintptr(fd), "neighbor")
	deadline, _ := ctx.Deadline()

	err = f.SetDeadline(deadline)
	if err != nil {
		discard(f.Close())

		return nil, err
	}

	return f, nil
}

func neighborQuery(f *os.File, request []byte) ([]byte, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}

	var socketErr error

	err = rc.Write(func(fd uintptr) bool {
		socketErr = unix.Sendto(int(fd), request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})

		return !retryable(socketErr)
	})
	if err != nil || socketErr != nil {
		return nil, errors.Join(err, socketErr)
	}

	buf := make([]byte, neighborReplySize)

	var (
		n, flags int
		from     unix.Sockaddr
	)

	err = rc.Read(func(fd uintptr) bool {
		n, _, flags, from, socketErr = unix.Recvmsg(int(fd), buf, nil, 0)

		return !retryable(socketErr)
	})
	if err != nil || socketErr != nil {
		return nil, errors.Join(err, socketErr)
	}

	sender, ok := from.(*unix.SockaddrNetlink)
	if !ok || sender.Pid != 0 || flags&unix.MSG_TRUNC != 0 {
		return nil, errors.New("invalid neighbor netlink response")
	}

	return buf[:n], nil
}

func neighborReply(
	raw []byte,
	family, ifindex int,
	ip net.IP,
) (net.HardwareAddr, neighState, error) {
	msgs, err := syscall.ParseNetlinkMessage(raw)
	if err != nil {
		return nil, neighMissing, fmt.Errorf("parse the neighbor reply: %w", err)
	}

	for i := range msgs {
		m := &msgs[i]
		if m.Header.Seq != 1 {
			return nil, neighMissing, errors.New("unexpected neighbor netlink sequence")
		}

		if m.Header.Type == unix.NLMSG_ERROR {
			err = neighborError(m.Data)
			if errors.Is(err, unix.ENOENT) {
				return nil, neighMissing, nil
			}

			return nil, neighMissing, err
		}

		if m.Header.Type == unix.RTM_NEWNEIGH {
			mac, state := neighEntry(m, family, ifindex, ip)

			return mac, state, nil
		}
	}

	return nil, neighMissing, errors.New("missing neighbor netlink response")
}

func neighborError(data []byte) error {
	var code int32

	_, err := binary.Decode(data, binary.NativeEndian, &code)
	if err != nil {
		return fmt.Errorf("decode the netlink error code: %w", err)
	}

	if code >= 0 {
		return errors.New("unexpected neighbor netlink acknowledgement")
	}

	return syscall.Errno(-int64(code))
}
