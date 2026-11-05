//go:build !darwin && !freebsd

package prober

import (
	"errors"
	"net"
)

// Linux sends DF through icmp_linux.go; other platforms reject it at construction.
func (*icmpPinger) listenDF(*net.IPAddr) (*nativeEchoConn, error) {
	return nil, errors.New("portable native DF is unavailable on this OS")
}
