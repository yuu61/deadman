//go:build darwin || freebsd

package prober

import (
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestBSDNativeEchoDFOption(t *testing.T) {
	for _, tc := range []struct {
		address string
		level   int
		option  int
	}{
		{"127.0.0.1", unix.IPPROTO_IP, unix.IP_DONTFRAG},
		{"::1", unix.IPPROTO_IPV6, unix.IPV6_DONTFRAG},
	} {
		for _, privileged := range []bool{false, true} {
			t.Run(tc.address+"/raw="+strconv.FormatBool(privileged), func(t *testing.T) {
				for _, df := range []bool{false, true} {
					t.Run("df="+strconv.FormatBool(df), func(t *testing.T) {
						p := &icmpPinger{
							privileged: privileged,
							echo:       probe.EchoOptions{Size: probe.PayloadBytes(8972), DF: df},
						}

						fd := openBSDTestEchoSocket(t, p, tc.address)
						defer unix.Close(fd)

						value, err := unix.GetsockoptInt(fd, tc.level, tc.option)
						if err != nil {
							t.Fatal(err)
						}

						if (value != 0) != df {
							t.Errorf("DF socket option = %d, want enabled=%v", value, df)
						}
					})
				}
			})
		}
	}
}
