package ping

import (
	"math"
	"regexp"
	"testing"
)

func TestParsePingOutput(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		success bool
		rtt     float64
		ttl     int
	}{
		{"linux_float", "64 bytes from 8.8.8.8: icmp_seq=1 ttl=116 time=1.23 ms", true, 1.23, 116},
		{"linux_int", "64 bytes from 8.8.8.8: icmp_seq=1 ttl=64 time=5 ms", true, 5, 64},
		{"ipv6_hlim", "16 bytes from 2001:db8::1, icmp_seq=1 hlim=58 time=2.50 ms", true, 2.5, 58},
		{"no_ttl", "round-trip time=1.00 ms", true, 1.0, -1},
		{"fail", "Request timeout for icmp_seq 0", false, 0, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ParsePingOutput(c.in)
			if res.Success != c.success {
				t.Fatalf("Success = %v, want %v", res.Success, c.success)
			}

			if c.success {
				if math.Abs(res.RTT-c.rtt) > 1e-9 {
					t.Errorf("RTT = %v, want %v", res.RTT, c.rtt)
				}

				if res.TTL != c.ttl {
					t.Errorf("TTL = %d, want %d", res.TTL, c.ttl)
				}
			} else if res.Code != Failed {
				t.Errorf("Code = %d, want Failed", res.Code)
			}
		})
	}
}

func TestParseRouterOSMinRTT(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"1ms500us", 1.5, true},
		{"500us", 0.5, true},
		{"2ms0us", 2.0, true},
		{"10ms250us", 10.25, true},
		// Whole-ms / seconds shapes the old mandatory-"us" regex dropped to (0,false).
		{"5ms", 5, true},
		{"2s", 2000, true},
		{"1s500ms", 1500, true},
		{"garbage", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseRouterOSMinRTT(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.in, ok, c.ok)

			continue
		}

		if ok && math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}

// The snmp/hping summary regexes must capture a fractional first value, not just an
// integer, so a sub-millisecond reply reports its real RTT instead of truncating to 0.
func TestSummaryRTTRegexCapturesFractional(t *testing.T) {
	cases := []struct {
		name string
		re   *regexp.Regexp
		in   string
		want string
	}{
		{"snmp_fractional", reSNMP, "rtt min/avg/max/stddev = 0.412/0.5/0.6/0.1 ms", "0.412"},
		{"snmp_integer", reSNMP, "rtt min/avg/max/stddev = 5/5/5/0 ms", "5"},
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
