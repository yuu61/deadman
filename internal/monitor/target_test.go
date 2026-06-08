package monitor

import (
	"math"
	"testing"

	"github.com/yuu61/deadman/internal/ping"
)

func TestGlyph(t *testing.T) {
	const scale = 10

	cases := []struct {
		name string
		res  ping.Result
		want string
	}{
		{"sub_scale", ping.Result{Success: true, RTT: 9}, "▁"},
		{"one_scale", ping.Result{Success: true, RTT: 10}, "▂"},
		{"near_seven", ping.Result{Success: true, RTT: 65}, "▇"},
		{"at_seven", ping.Result{Success: true, RTT: 70}, "█"},
		{"over", ping.Result{Success: true, RTT: 1000}, "█"},
		{"failed", ping.Result{Code: ping.Failed}, "X"},
		{"ssh_timeout", ping.Result{Code: ping.SSHTimeout}, "t"},
		{"ssh_failed", ping.Result{Code: ping.SSHFailed}, "s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := glyph(c.res, scale); got != c.want {
				t.Errorf("glyph(%+v) = %q, want %q", c.res, got, c.want)
			}
		})
	}
}

func TestConsume(t *testing.T) {
	tg := &Target{scale: 10}
	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 10, TTL: 50})
	tg.Consume(ping.Result{Code: ping.Failed})
	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 30, TTL: 51})

	if tg.Snt != 3 {
		t.Errorf("Snt = %d, want 3", tg.Snt)
	}

	if tg.Loss != 1 {
		t.Errorf("Loss = %d, want 1", tg.Loss)
	}

	if math.Abs(tg.LossRate-100.0/3.0) > 1e-9 {
		t.Errorf("LossRate = %v, want %v", tg.LossRate, 100.0/3.0)
	}
	// avg = sum(success RTT) / total sent = (10+30)/3.
	if math.Abs(tg.Avg-40.0/3.0) > 1e-9 {
		t.Errorf("Avg = %v, want %v", tg.Avg, 40.0/3.0)
	}

	if tg.State != Up {
		t.Errorf("State = %v, want Up", tg.State)
	}
	// History is newest-first: last result was RTT 30 (scale 10 -> ▄).
	if got := tg.Glyphs(1); len(got) != 1 || got[0] != "▄" {
		t.Errorf("Glyphs(1) = %v, want [▄]", got)
	}

	if len(tg.Glyphs(10)) != 3 {
		t.Errorf("history length = %d, want 3", len(tg.Glyphs(10)))
	}
}

func TestRefresh(t *testing.T) {
	tg := &Target{scale: 10}
	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 5})
	tg.Refresh()

	if tg.Snt != 0 || tg.Loss != 0 || tg.State != Unknown || len(tg.Glyphs(10)) != 0 {
		t.Errorf("after Refresh: %+v history=%v", tg, tg.Glyphs(10))
	}
}

func TestKeyStableAndMatches(t *testing.T) {
	a := &Target{
		Name:  "n",
		Addr:  "1.2.3.4",
		Relay: map[string]string{"via": "snmp", "relay": "5.6.7.8", "community": "c"},
	}

	b := &Target{
		Name:  "n",
		Addr:  "1.2.3.4",
		Relay: map[string]string{"community": "c", "relay": "5.6.7.8", "via": "snmp"},
	}
	if a.Key() != b.Key() {
		t.Errorf("Key mismatch despite same data:\n a=%q\n b=%q", a.Key(), b.Key())
	}

	c := &Target{
		Name:  "n",
		Addr:  "1.2.3.4",
		Relay: map[string]string{"via": "snmp", "relay": "5.6.7.8", "community": "different"},
	}
	if a.Key() == c.Key() {
		t.Error("Key collision for differing community")
	}
}
