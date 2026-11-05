package prober

import (
	"net"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestLinuxDFSocketOptions(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		level  int
		option int
		want   int
	}{
		{"127.0.0.1", unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO},
		{"::1", unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_DO},
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
		})
	}
}
