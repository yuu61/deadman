package prober

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestLinuxDFSocketOptions(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		level  int
		option int
		recv   int
		want   int
	}{
		{"127.0.0.1", unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_RECVERR, unix.IP_PMTUDISC_DO},
		{"::1", unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_RECVERR, unix.IPV6_PMTUDISC_DO},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			p := &icmpPinger{echo: probe.EchoOptions{DF: true}}

			conn, err := p.dial(familyOf(net.ParseIP(tc.addr)), unix.SOCK_DGRAM, 1)
			if err != nil {
				t.Skipf("ICMP datagram socket unavailable: %v", err)
			}
			defer conn.f.Close()

			var (
				value     int
				optionErr error
			)

			err = conn.rc.Control(func(fd uintptr) {
				value, optionErr = unix.GetsockoptInt(int(fd), tc.level, tc.option)
			})
			if err != nil || optionErr != nil || value != tc.want {
				t.Fatalf("DF option = %d, %v, %v; want %d", value, err, optionErr, tc.want)
			}

			err = conn.rc.Control(func(fd uintptr) {
				value, optionErr = unix.GetsockoptInt(int(fd), tc.level, tc.recv)
			})
			if err != nil || optionErr != nil || value != 1 {
				t.Fatalf("RECVERR option = %d, %v, %v; want 1", value, err, optionErr)
			}
		})
	}
}

func TestLinuxSkipNonMTUErrorOnEmptyQueue(t *testing.T) {
	p := &icmpPinger{echo: probe.EchoOptions{DF: true}}

	conn, err := p.dial(familyOf(net.ParseIP("127.0.0.1")), unix.SOCK_DGRAM, 1)
	if err != nil {
		t.Skipf("ICMP datagram socket unavailable: %v", err)
	}
	defer conn.f.Close()

	pr := icmpProbe{
		fam:   familyOf(net.ParseIP("127.0.0.1")),
		df:    true,
		id:    1,
		seq:   1,
		token: echoPayload(24),
	}

	if !pr.skipNonMTUError(conn.rc, net.ParseIP("127.0.0.1"), unix.EHOSTUNREACH) {
		t.Fatal("skipNonMTUError returned false on an empty error queue")
	}
}

func extendedErrorOOB(t *testing.T, family icmpFamily, extended unix.SockExtendedErr) []byte {
	t.Helper()

	header := unix.Cmsghdr{Level: unix.IPPROTO_IPV6, Type: unix.IPV6_RECVERR}
	if family.domain == unix.AF_INET {
		header.Level, header.Type = unix.IPPROTO_IP, unix.IP_RECVERR
	}

	size := binary.Size(extended)
	header.SetLen(unix.CmsgLen(size))
	oob := make([]byte, unix.CmsgSpace(size))

	_, err := binary.Encode(oob, binary.NativeEndian, header)
	if err != nil {
		t.Fatal(err)
	}

	_, err = binary.Encode(oob[unix.CmsgLen(0):], binary.NativeEndian, extended)
	if err != nil {
		t.Fatal(err)
	}

	return oob
}

func TestLinuxQueuedErrorMatching(t *testing.T) {
	for _, address := range []string{"192.0.2.1", "2001:db8::1"} {
		for _, privileged := range []bool{false, true} {
			peer := net.ParseIP(address)
			pr := icmpProbe{
				fam:        familyOf(peer),
				privileged: privileged,
				id:         7,
				seq:        1,
				token:      echoPayload(2000),
			}

			dest, err := ipSockaddr(peer, 0)
			if err != nil {
				t.Fatal(err)
			}

			wire, err := pr.build()
			if err != nil {
				t.Fatal(err)
			}

			for _, other := range otherICMPErrors(pr.fam) {
				oob := extendedErrorOOB(t, pr.fam, other)
				if pr.matchQueuedError(wire, oob, dest, peer) {
					t.Fatalf("%s raw=%v: non-MTU error matched: %+v", address, privileged, other)
				}
			}

			for _, extended := range queuedICMPErrors(pr.fam) {
				oob := extendedErrorOOB(t, pr.fam, extended)
				for _, length := range []int{8, 12, len(wire)} {
					if !pr.matchQueuedError(wire[:length], oob, dest, peer) {
						t.Fatalf(
							"%s raw=%v: matching queued error with %d quoted bytes rejected: %+v",
							address,
							privileged,
							length,
							extended,
						)
					}
				}

				foreignID := bytes.Clone(wire)
				foreignID[4] ^= 1

				if pr.matchQueuedError(foreignID, oob, dest, peer) == privileged {
					t.Fatalf("%s raw=%v: wrong identifier matching", address, privileged)
				}

				for _, offset := range []int{0, 1, 6, 8, len(wire) - 1} {
					foreign := bytes.Clone(wire)
					foreign[offset] ^= 1

					if pr.matchQueuedError(foreign, oob, dest, peer) {
						t.Fatalf(
							"%s raw=%v: altered request byte %d accepted",
							address,
							privileged,
							offset,
						)
					}
				}

				if pr.matchQueuedError(wire[:7], oob, dest, peer) ||
					pr.matchQueuedError(wire, oob, nil, peer) ||
					pr.matchQueuedError(wire, oob, dest, net.ParseIP("192.0.2.2")) ||
					pr.matchQueuedError(wire, []byte{1}, dest, peer) {
					t.Fatal("truncated or unrelated queued error accepted")
				}

				for _, wrong := range []unix.SockExtendedErr{
					{Origin: extended.Origin, Type: extended.Type, Code: extended.Code},
					{Errno: extended.Errno, Origin: unix.SO_EE_ORIGIN_NONE, Type: extended.Type, Code: extended.Code},
				} {
					if pr.matchQueuedError(wire, extendedErrorOOB(t, pr.fam, wrong), dest, peer) {
						t.Fatalf("unrelated extended error accepted: %+v", wrong)
					}
				}

				// A normal received echo must not be mistaken for a queued error.
				reply := icmp.Message{
					Type: pr.fam.replyType,
					Body: &icmp.Echo{ID: pr.id, Seq: pr.seq, Data: pr.token},
				}

				data, err := reply.Marshal(nil)
				if err != nil {
					t.Fatal(err)
				}

				if pr.matchQueuedError(data, oob, dest, peer) {
					t.Fatal("echo reply accepted as a queued error")
				}
			}
		}
	}
}

func queuedICMPErrors(family icmpFamily) []unix.SockExtendedErr {
	if family.domain == unix.AF_INET {
		return []unix.SockExtendedErr{
			{
				Errno: uint32(unix.EMSGSIZE), Origin: unix.SO_EE_ORIGIN_ICMP,
				Type: uint8(ipv4.ICMPTypeDestinationUnreachable), Code: 4, Info: 1500,
			},
			{Errno: uint32(unix.EMSGSIZE), Origin: unix.SO_EE_ORIGIN_LOCAL},
		}
	}

	return []unix.SockExtendedErr{
		{
			Errno: uint32(unix.EMSGSIZE), Origin: unix.SO_EE_ORIGIN_ICMP6,
			Type: uint8(ipv6.ICMPTypePacketTooBig), Info: 1500,
		},
		{Errno: uint32(unix.EMSGSIZE), Origin: unix.SO_EE_ORIGIN_LOCAL},
	}
}

// otherICMPErrors are ICMP errors for the echo that are not about its size: the target did
// not answer, as without df.
func otherICMPErrors(family icmpFamily) []unix.SockExtendedErr {
	if family.domain == unix.AF_INET {
		return []unix.SockExtendedErr{
			{
				Errno: uint32(unix.EHOSTUNREACH), Origin: unix.SO_EE_ORIGIN_ICMP,
				Type: uint8(ipv4.ICMPTypeDestinationUnreachable), Code: 1,
			},
			{
				Errno: uint32(unix.ECONNREFUSED), Origin: unix.SO_EE_ORIGIN_ICMP,
				Type: uint8(ipv4.ICMPTypeDestinationUnreachable), Code: 3,
			},
			{
				Errno: uint32(unix.EHOSTUNREACH), Origin: unix.SO_EE_ORIGIN_ICMP,
				Type: uint8(ipv4.ICMPTypeTimeExceeded),
			},
		}
	}

	return []unix.SockExtendedErr{
		{
			Errno: uint32(unix.EHOSTUNREACH), Origin: unix.SO_EE_ORIGIN_ICMP6,
			Type: uint8(ipv6.ICMPTypeDestinationUnreachable), Code: 3,
		},
		{
			Errno: uint32(unix.ECONNREFUSED), Origin: unix.SO_EE_ORIGIN_ICMP6,
			Type: uint8(ipv6.ICMPTypeDestinationUnreachable), Code: 4,
		},
		{
			Errno: uint32(unix.EHOSTUNREACH), Origin: unix.SO_EE_ORIGIN_ICMP6,
			Type: uint8(ipv6.ICMPTypeTimeExceeded),
		},
	}
}
