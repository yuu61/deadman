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

func mtuErrorOOB(t *testing.T, family icmpFamily, extended unix.SockExtendedErr) []byte {
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

func TestLinuxMTUErrorMatching(t *testing.T) {
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

			extended := unix.SockExtendedErr{
				Errno: uint32(unix.EMSGSIZE), Origin: unix.SO_EE_ORIGIN_ICMP6,
				Type: uint8(ipv6.ICMPTypePacketTooBig), Info: 1500,
			}
			if pr.fam.domain == unix.AF_INET {
				extended.Origin, extended.Type, extended.Code = unix.SO_EE_ORIGIN_ICMP,
					uint8(ipv4.ICMPTypeDestinationUnreachable), 4
			}

			oob := mtuErrorOOB(t, pr.fam, extended)
			for _, length := range []int{8, 12, len(wire)} {
				if !pr.matchMTUError(wire[:length], oob, dest, peer) {
					t.Fatalf(
						"%s raw=%v: matching MTU error with %d quoted bytes rejected",
						address,
						privileged,
						length,
					)
				}
			}

			foreignID := bytes.Clone(wire)
			foreignID[4] ^= 1

			if pr.matchMTUError(foreignID, oob, dest, peer) == privileged {
				t.Fatalf("%s raw=%v: wrong identifier matching", address, privileged)
			}

			for _, offset := range []int{0, 1, 6, 8, len(wire) - 1} {
				foreign := bytes.Clone(wire)
				foreign[offset] ^= 1

				if pr.matchMTUError(foreign, oob, dest, peer) {
					t.Fatalf(
						"%s raw=%v: altered request byte %d accepted",
						address,
						privileged,
						offset,
					)
				}
			}

			if pr.matchMTUError(wire[:7], oob, dest, peer) ||
				pr.matchMTUError(wire, oob, nil, peer) ||
				pr.matchMTUError(wire, oob, dest, net.ParseIP("192.0.2.2")) ||
				pr.matchMTUError(wire, []byte{1}, dest, peer) {
				t.Fatal("truncated or unrelated MTU error accepted")
			}

			for _, wrong := range []unix.SockExtendedErr{
				{Errno: uint32(unix.EHOSTUNREACH), Origin: extended.Origin, Type: extended.Type, Code: extended.Code},
				{Errno: extended.Errno, Origin: unix.SO_EE_ORIGIN_LOCAL, Type: extended.Type, Code: extended.Code},
				{Errno: extended.Errno, Origin: extended.Origin, Type: extended.Type + 1, Code: extended.Code},
				{Errno: extended.Errno, Origin: extended.Origin, Type: extended.Type, Code: extended.Code + 1},
			} {
				if pr.matchMTUError(wire, mtuErrorOOB(t, pr.fam, wrong), dest, peer) {
					t.Fatalf("unrelated extended error accepted: %+v", wrong)
				}
			}

			// A normal received echo must not be mistaken for a queued MTU error.
			reply := icmp.Message{
				Type: pr.fam.replyType,
				Body: &icmp.Echo{ID: pr.id, Seq: pr.seq, Data: pr.token},
			}

			data, err := reply.Marshal(nil)
			if err != nil {
				t.Fatal(err)
			}

			if pr.matchMTUError(data, oob, dest, peer) {
				t.Fatal("echo reply accepted as an MTU error")
			}
		}
	}
}
