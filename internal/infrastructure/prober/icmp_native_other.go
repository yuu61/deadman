//go:build !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package prober

import (
	"errors"
	"net"
)

const nativeBSDEcho = false

// Linux sends through icmp_linux.go; other platforms use pro-bing or sendScoped.
func (*icmpPinger) listenNativeEcho(*net.IPAddr) (*nativeEchoConn, error) {
	return nil, errors.New("BSD native ICMP is unavailable on this OS")
}
