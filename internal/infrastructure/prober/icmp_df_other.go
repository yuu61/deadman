//go:build dragonfly || netbsd || openbsd

package prober

import (
	"errors"
	"net"
)

// These BSDs support native echo sockets but reject DF at construction.
func setNativeDF(int, net.IP) error {
	return errors.New("portable native DF is unavailable on this OS")
}
