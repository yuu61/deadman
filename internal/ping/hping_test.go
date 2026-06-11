package ping

import "testing"

// hping3 prints its "round-trip min/avg/max = 0.0/0.0/0.0 ms" summary even on 100%
// loss, so liveness must gate on the received-packet count, not on that summary. A
// down/filtered tcp target must read as Failed, not a green 0 ms reply.
func TestParseHpingResult(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		wantSuccess bool
		wantRTT     float64
	}{
		{
			// 100% loss: hping3 still prints the zeroed round-trip summary. Note hping3's
			// own typo "tramitted" in the transmit word.
			name: "loss_100",
			out: "HPING 1.2.3.4 (eth0 1.2.3.4): S set, 40 headers + 0 data bytes\n" +
				"--- 1.2.3.4 hping statistic ---\n" +
				"1 packets tramitted, 0 packets received, 100% packet loss\n" +
				"round-trip min/avg/max = 0.0/0.0/0.0 ms\n",
			wantSuccess: false,
		},
		{
			name: "reply",
			out: "len=46 ip=1.2.3.4 ttl=58 DF id=0 sport=80 flags=SA seq=0 win=64240 rtt=12.3 ms\n" +
				"--- 1.2.3.4 hping statistic ---\n" +
				"1 packets tramitted, 1 packets received, 0% packet loss\n" +
				"round-trip min/avg/max = 12.3/12.3/12.3 ms\n",
			wantSuccess: true,
			wantRTT:     12.3,
		},
		{
			name:        "empty",
			out:         "",
			wantSuccess: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := parseHpingResult(c.out)
			if res.Success != c.wantSuccess {
				t.Fatalf("Success = %v, want %v (res=%+v)", res.Success, c.wantSuccess, res)
			}

			if c.wantSuccess && res.RTT != c.wantRTT {
				t.Errorf("RTT = %v, want %v", res.RTT, c.wantRTT)
			}

			if !c.wantSuccess && res.Code != Failed {
				t.Errorf("Code = %v, want Failed", res.Code)
			}
		})
	}
}
