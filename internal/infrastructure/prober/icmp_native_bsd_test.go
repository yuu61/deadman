//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package prober

import (
	"errors"
	"net"
	"runtime"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestBSDNativeEchoSocketBuffers(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		for _, privileged := range []bool{false, true} {
			t.Run(address+"/raw="+strconv.FormatBool(privileged), func(t *testing.T) {
				p := &icmpPinger{
					privileged: privileged,
					echo:       probe.EchoOptions{Size: probe.PayloadBytes(8972)},
				}

				fd := openBSDTestEchoSocket(t, p, address)
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

func TestBSDPortableJumboLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		for _, privileged := range []bool{false, true} {
			t.Run(address+"/raw="+strconv.FormatBool(privileged), func(t *testing.T) {
				p := &icmpPinger{
					dest:       compiled(t, probe.Spec{Addr: address}).Destination(),
					privileged: privileged,
					echo:       probe.EchoOptions{Size: probe.PayloadBytes(8972)},
				}

				// Check support and permissions before requiring a successful probe.
				fd := openBSDTestEchoSocket(t, p, address)

				err := unix.Close(fd)
				if err != nil {
					t.Fatal(err)
				}

				if result := p.sendPortable(t.Context()); !result.IsSuccess() {
					t.Fatalf("jumbo loopback: %+v", result)
				}
			})
		}
	}
}

func openBSDTestEchoSocket(t *testing.T, p *icmpPinger, address string) int {
	t.Helper()

	if !p.privileged && runtime.GOOS != "darwin" {
		t.Skip("datagram ICMP sockets are only supported on Darwin")
	}

	fd, err := p.nativeEchoSocket(&net.IPAddr{IP: net.ParseIP(address)})
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		t.Skipf("ICMP socket requires privileges: %v", err)
	}

	if err != nil {
		t.Fatal(err)
	}

	return fd
}
