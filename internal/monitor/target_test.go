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
			if got := Glyph(c.res, scale); got != c.want {
				t.Errorf("Glyph(%+v) = %q, want %q", c.res, got, c.want)
			}
		})
	}
}

func TestConsume(t *testing.T) {
	tg := &Target{}
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
	// min/max over the successful RTTs (10, 30).
	if tg.Min != 10 || tg.Max != 30 {
		t.Errorf("Min/Max = %v/%v, want 10/30", tg.Min, tg.Max)
	}
	// Jitter is the RFC 3550 EWMA of |ΔRTT| over consecutive successes: the first
	// success (RTT 10) only seeds prevRTT; the second (RTT 30) gives
	// Jit = 0 + (|30-10| - 0)/16 = 1.25. The failure between them is skipped.
	if math.Abs(tg.Jit-1.25) > 1e-9 {
		t.Errorf("Jit = %v, want 1.25", tg.Jit)
	}
	// History is newest-first: the last result was RTT 30, which renders ▄ at scale 10.
	if got := tg.Results(1); len(got) != 1 || Glyph(got[0], 10) != "▄" {
		t.Errorf("Results(1) rendered at scale 10 = %v, want [▄]", got)
	}

	if len(tg.Results(10)) != 3 {
		t.Errorf("history length = %d, want 3", len(tg.Results(10)))
	}
}

func TestRefresh(t *testing.T) {
	tg := &Target{}
	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 5})
	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 25})
	tg.Refresh()

	if tg.Snt != 0 || tg.Loss != 0 || tg.State != Unknown || len(tg.Results(10)) != 0 {
		t.Errorf("after Refresh: %+v history=%v", tg, tg.Results(10))
	}
	// The new running stats must reset too, or a refreshed target keeps stale
	// min/max/jitter (the fields above do not cover them).
	if tg.Min != 0 || tg.Max != 0 || tg.Jit != 0 || tg.prevRTT != 0 {
		t.Errorf("after Refresh: Min=%v Max=%v Jit=%v prevRTT=%v, want all 0",
			tg.Min, tg.Max, tg.Jit, tg.prevRTT)
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

// TestKeyResolveFamilySeparates guards the dual-stack use case: two rows for the
// same hostname differing only in resolve_family must get distinct Keys, so a reload
// keeps their history apart instead of collapsing the A and AAAA rows into one.
func TestKeyResolveFamilySeparates(t *testing.T) {
	v4 := &Target{
		Name:  "web",
		Addr:  "example.com",
		Relay: map[string]string{"resolve_family": "ipv4"},
	}
	v6 := &Target{
		Name:  "web",
		Addr:  "example.com",
		Relay: map[string]string{"resolve_family": "ipv6"},
	}
	auto := &Target{Name: "web", Addr: "example.com", Relay: map[string]string{}}

	keys := map[string]string{"v4": v4.Key(), "v6": v6.Key(), "auto": auto.Key()}
	for a := range keys {
		for b := range keys {
			if a < b && keys[a] == keys[b] {
				t.Errorf("Key collision between %s and %s: %q", a, b, keys[a])
			}
		}
	}
}

// TestResultsRescale shows the result bar re-buckets when the scale changes: the
// same stored result renders to a different glyph at a different scale. This is what
// lets the up/down keys re-scale bars already on screen.
func TestResultsRescale(t *testing.T) {
	tg := &Target{}
	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 15})

	res := tg.Results(1)[0]
	// RTT 15: at scale 10 it lands in the 2nd bucket (10..20 -> ▂); at scale 5 it is
	// in the 4th (15 == 5*3, < 5*4 -> ▄).
	if got := Glyph(res, 10); got != "▂" {
		t.Errorf("Glyph(RTT 15, scale 10) = %q, want ▂", got)
	}

	if got := Glyph(res, 5); got != "▄" {
		t.Errorf("Glyph(RTT 15, scale 5) = %q, want ▄", got)
	}
}
