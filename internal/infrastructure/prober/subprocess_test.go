package prober

import (
	"math"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestParsePingOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code probe.ResultCode
		rtt  float64
	}{
		{
			"linux_float",
			"64 bytes from 8.8.8.8: icmp_seq=1 ttl=116 time=1.23 ms",
			probe.Success,
			1.23,
		},
		{"linux_int", "64 bytes from 8.8.8.8: icmp_seq=1 ttl=64 time=5 ms", probe.Success, 5},
		{
			"ipv6_hlim",
			"16 bytes from 2001:db8::1, icmp_seq=1 hlim=58 time=2.50 ms",
			probe.Success,
			2.5,
		},
		{"no_ttl", "round-trip time=1.00 ms", probe.Success, 1.0},
		{"linux_no_reply", "1 packets transmitted, 0 received, 100% packet loss", probe.Failed, 0},
		{
			"bsd_no_reply",
			"1 packets transmitted, 0 packets received, 100.0% packet loss",
			probe.Failed,
			0,
		},
		{"wrapper_error", "Request timeout for icmp_seq 0", probe.Unavailable, 0},
		{"nothing_sent", "0 packets transmitted, 0 packets received", probe.Unavailable, 0},
		{"zero_rtt", "64 bytes from 192.0.2.1: time=0.000 ms", probe.Success, 0},
		{"empty", "", probe.Unavailable, 0},
		{"negative_rtt", "time=-1 ms", probe.Unavailable, 0},
		{"invalid_rtt", "time=NaN ms", probe.Unavailable, 0},
		{"overflow_rtt", "time=" + strings.Repeat("9", 400) + " ms", probe.Unavailable, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := parsePingOutput(c.in)
			if res.Code != c.code {
				t.Fatalf("Code = %d, want %d", res.Code, c.code)
			}

			if c.code == probe.Success {
				if !(math.Abs(res.RTT-c.rtt) <= 1e-9) {
					t.Errorf("RTT = %v, want %v", res.RTT, c.rtt)
				}
			}
		})
	}
}

// ssh must never prompt on the terminal the TUI owns, and must refuse a relay whose host
// key changed.
func TestSSHArgsDoNotPrompt(t *testing.T) {
	args := strings.Join(sshArgs(probe.SSH{Host: "relay"}), " ")

	for _, want := range []string{"-o BatchMode=yes", "-o StrictHostKeyChecking=accept-new"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh argv %q lacks %q", args, want)
		}
	}
}
