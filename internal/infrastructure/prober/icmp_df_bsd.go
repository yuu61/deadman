//go:build darwin || freebsd

package prober

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func setNativeDF(fd int, dest net.IP) error {
	level, option := unix.IPPROTO_IPV6, unix.IPV6_DONTFRAG
	if dest.To4() != nil {
		level, option = unix.IPPROTO_IP, unix.IP_DONTFRAG
	}

	err := unix.SetsockoptInt(fd, level, option, 1)
	if err != nil {
		return fmt.Errorf("set ICMP do-not-fragment: %w", err)
	}

	return nil
}
