package prober

import (
	"fmt"
	"syscall"

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
	if family.domain == unix.AF_INET {
		level, option, value = unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO
	}

	var optionErr error

	err := conn.Control(func(fd uintptr) {
		optionErr = unix.SetsockoptInt(int(fd), level, option, value)
	})
	if err != nil {
		return err
	}

	if optionErr != nil {
		return fmt.Errorf("set ICMP do-not-fragment: %w", optionErr)
	}

	return nil
}
