//go:build linux

package prober

import (
	"fmt"
	"math"

	"golang.org/x/net/bpf"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const echoIDOffset = 4

// echoFilter drops foreign ICMP before it reaches a raw socket's receive queue.
// IPv4 raw sockets include the variable-length IP header; IPv6 starts at ICMP.
// Userspace still verifies the peer, sequence and token.
func echoFilter(proto, id int) ([]bpf.RawInstruction, error) {
	if id < 0 || id > math.MaxUint16 {
		return nil, fmt.Errorf("invalid ICMP identifier %d", id)
	}

	var offset bpf.Instruction = bpf.LoadConstant{Dst: bpf.RegX, Val: 0}

	replyType := uint32(ipv6.ICMPTypeEchoReply)

	if proto == ianaProtocolICMP {
		offset = bpf.LoadMemShift{Off: 0}
		replyType = uint32(ipv4.ICMPTypeEchoReply)
	}

	raw, err := bpf.Assemble([]bpf.Instruction{
		offset,
		bpf.LoadIndirect{Off: 0, Size: 1},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: replyType, SkipTrue: 1},
		bpf.RetConstant{Val: 0},
		bpf.LoadIndirect{Off: echoIDOffset, Size: 2},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(id), SkipFalse: 1},
		bpf.RetConstant{Val: math.MaxUint32},
		bpf.RetConstant{Val: 0},
	})
	if err != nil {
		return nil, fmt.Errorf("assemble the ICMP echo filter: %w", err)
	}

	return raw, nil
}

func filterICMPSocket(fd, proto, id int) error {
	filter, err := echoFilter(proto, id)
	if err != nil {
		return err
	}

	code := make([]unix.SockFilter, len(filter))
	for i, ins := range filter {
		code[i] = unix.SockFilter{Code: ins.Op, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
	}

	n := len(code)
	if n == 0 || n > math.MaxUint16 {
		return fmt.Errorf("invalid ICMP filter length %d", n)
	}

	program := unix.SockFprog{Len: uint16(n), Filter: &code[0]}

	err = unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &program)
	if err != nil {
		return fmt.Errorf("attach the ICMP echo filter: %w", err)
	}

	return nil
}

func filterEcho(conn *icmp.PacketConn, id int) error {
	v4 := conn.IPv4PacketConn()

	proto := ianaProtocolICMPv6
	if v4 != nil {
		proto = ianaProtocolICMP
	}

	filter, err := echoFilter(proto, id)
	if err != nil {
		return err
	}

	if v4 != nil {
		err = v4.SetBPF(filter)
	} else {
		err = conn.IPv6PacketConn().SetBPF(filter)
	}

	if err != nil {
		return fmt.Errorf("attach the ICMP echo filter: %w", err)
	}

	return nil
}
