//go:build darwin || freebsd

package prober

import (
	"errors"
	"net"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestBSDDFSocketBuffers(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		for _, privileged := range []bool{false, true} {
			t.Run(address+"/raw="+strconv.FormatBool(privileged), func(t *testing.T) {
				p := &icmpPinger{
					privileged: privileged,
					echo:       probe.EchoOptions{Size: probe.PayloadBytes(8972), DF: true},
				}

				fd, err := p.dfSocket(&net.IPAddr{IP: net.ParseIP(address)})
				if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
					t.Skipf("ICMP socket requires privileges: %v", err)
				}

				if err != nil {
					t.Fatal(err)
				}

				defer unix.Close(fd)

				for _, option := range []int{unix.SO_SNDBUF, unix.SO_RCVBUF} {
					size, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, option)
					if err != nil {
						t.Fatal(err)
					}

					if size < echoSocketBufferSize(p.echo.Size.Bytes()) {
						t.Errorf("socket option %d = %d, too small for a jumbo echo", option, size)
					}
				}
			})
		}
	}
}
