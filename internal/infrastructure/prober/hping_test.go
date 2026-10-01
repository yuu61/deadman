package prober

import (
	"regexp"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// hping3 prints its "round-trip min/avg/max = 0.0/0.0/0.0 ms" summary even on 100%
// loss, so liveness must gate on the received-packet count, not on that summary. A
// down/filtered tcp target must read as Failed, not a green 0 ms reply.
func TestParseHpingResult(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		wantCode probe.ResultCode
		wantRTT  float64
	}{
		{
			// 100% loss: hping3 still prints the zeroed round-trip summary. Note hping3's
			// own typo "tramitted" in the transmit word.
			name: "loss_100",
			out: "HPING 1.2.3.4 (eth0 1.2.3.4): S set, 40 headers + 0 data bytes\n" +
				"--- 1.2.3.4 hping statistic ---\n" +
				"1 packets tramitted, 0 packets received, 100% packet loss\n" +
				"round-trip min/avg/max = 0.0/0.0/0.0 ms\n",
			wantCode: probe.Failed,
		},
		{
			name: "reply",
			out: "len=46 ip=1.2.3.4 ttl=58 DF id=0 sport=80 flags=SA seq=0 win=64240 rtt=12.3 ms\n" +
				"--- 1.2.3.4 hping statistic ---\n" +
				"1 packets tramitted, 1 packets received, 0% packet loss\n" +
				"round-trip min/avg/max = 12.3/12.3/12.3 ms\n",
			wantCode: probe.Success,
			wantRTT:  12.3,
		},
		{
			name: "router_icmp_error",
			out: "ICMP Unreachable type 3 code 0 from ip=192.0.2.254 name=router\n" +
				"1 packets tramitted, 1 packets received, 0% packet loss\n" +
				"round-trip min/avg/max = 0.4/0.4/0.4 ms\n",
			wantCode: probe.Unavailable,
		},
		{
			name:     "empty",
			out:      "",
			wantCode: probe.Unavailable,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := parseHpingResult(c.out)
			if res.Code != c.wantCode {
				t.Fatalf("Code = %v, want %v (res=%+v)", res.Code, c.wantCode, res)
			}

			if c.wantCode == probe.Success && res.RTT != c.wantRTT {
				t.Errorf("RTT = %v, want %v", res.RTT, c.wantRTT)
			}
		})
	}
}

// The hping summary regex must capture a fractional first value, not just an
// integer, so a sub-millisecond reply reports its real RTT instead of truncating to 0.
func TestSummaryRTTRegexCapturesFractional(t *testing.T) {
	cases := []struct {
		name string
		re   *regexp.Regexp
		in   string
		want string
	}{
		{"hping_fractional", reHping, "round-trip min/avg/max = 0.4/0.4/0.4 ms", "0.4"},
		{"hping_integer", reHping, "round-trip min/avg/max = 6/6/6 ms", "6"},
	}
	for _, c := range cases {
		m := c.re.FindStringSubmatch(c.in)
		if len(m) < 2 || m[1] != c.want {
			t.Errorf("%s: %q -> %v, want capture %q", c.name, c.in, m, c.want)
		}
	}
}
