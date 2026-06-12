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
			if got := Glyph(c.res, scale, 0); got != c.want {
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
	if got := tg.Results(1); len(got) != 1 || Glyph(got[0], 10, 0) != "▄" {
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

// TestKeyDelimiterCollision guards the escaping: two structurally different targets
// must not produce the same Key just because a field value contains the ':'/'=' that
// Key uses as delimiters. Without quoting, {user:"bob", via:"ssh"} and
// {user:"bob:via=ssh"} both flatten to "...:user=bob:via=ssh" and would alias each
// other's history across a reload.
func TestKeyDelimiterCollision(t *testing.T) {
	twoKeys := &Target{
		Name:  "web",
		Addr:  "1.1.1.1",
		Relay: map[string]string{"user": "bob", "via": "ssh"},
	}
	oneKey := &Target{
		Name:  "web",
		Addr:  "1.1.1.1",
		Relay: map[string]string{"user": "bob:via=ssh"},
	}

	if twoKeys.Key() == oneKey.Key() {
		t.Errorf("Key collision across a forged delimiter:\n %q\n %q", twoKeys.Key(), oneKey.Key())
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
	if got := Glyph(res, 10, 0); got != "▂" {
		t.Errorf("Glyph(RTT 15, scale 10) = %q, want ▂", got)
	}

	if got := Glyph(res, 5, 0); got != "▄" {
		t.Errorf("Glyph(RTT 15, scale 5) = %q, want ▄", got)
	}
}

// TestRttGlyphLog verifies logarithmic bucketing (logK > 0): band i covers
// [floor*aⁱ, floor*a^(i+1)) with a = e^logK and floor = scale, so an RTT exactly on
// a band boundary (floor*e^(logK*i)) lands in band i (glyph rttBars[i]) and one past
// the last band overflows to "█". Boundary inputs are generated with math.Exp so the
// floating-point edges (which boundaryEpsilon repairs) are exercised.
func TestRttGlyphLog(t *testing.T) {
	exp := math.Exp

	cases := []struct {
		name  string
		rtt   float64
		scale float64
		logK  int
		want  string
	}{
		// logK=1 (base e), floor 1.0 — exact band boundaries.
		{"k1_below_floor", 0.5, 1.0, 1, "▁"},
		{"k1_at_floor", 1.0, 1.0, 1, "▁"},
		{"k1_e1", exp(1), 1.0, 1, "▂"},
		{"k1_e2", exp(2), 1.0, 1, "▃"},
		{"k1_e3", exp(3), 1.0, 1, "▄"},
		{"k1_e4", exp(4), 1.0, 1, "▅"},
		{"k1_e5", exp(5), 1.0, 1, "▆"},
		{"k1_e6", exp(6), 1.0, 1, "▇"},
		{"k1_e7", exp(7), 1.0, 1, "█"},
		{"k1_huge", 1e9, 1.0, 1, "█"},
		// Mid-band (not on a boundary) lands in the lower band: floor(2.5) = 2.
		{"k1_mid_2_3", exp(2.5), 1.0, 1, "▃"},

		// logK=2 (base e²), floor 1.0.
		{"k2_at_floor", 1.0, 1.0, 2, "▁"},
		{"k2_e2", exp(2), 1.0, 2, "▂"},
		{"k2_e4", exp(4), 1.0, 2, "▃"},
		{"k2_e6", exp(6), 1.0, 2, "▄"},
		{"k2_e8", exp(8), 1.0, 2, "▅"},
		{"k2_e10", exp(10), 1.0, 2, "▆"},
		{"k2_e12", exp(12), 1.0, 2, "▇"},
		{"k2_e14", exp(14), 1.0, 2, "█"},

		// Non-unit floors: the exact-boundary inputs the epsilon guard repairs.
		{"floor03_e2", 0.3 * exp(2), 0.3, 1, "▃"},
		{"floor25_e4", 2.5 * exp(4), 2.5, 1, "▅"},
		{"subms_floor_e3", 0.001 * exp(3), 0.001, 1, "▄"},

		// Guards: zero/negative RTT and a degenerate floor fall back to ▁.
		{"zero_rtt", 0.0, 1.0, 1, "▁"},
		{"neg_floor", exp(3), -1.0, 1, "▁"},

		// logK=0 stays linear even through this table.
		{"k0_linear_9_scale10", 9.0, 10.0, 0, "▁"},
		{"k0_linear_70_scale10", 70.0, 10.0, 0, "█"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ping.Result{Success: true, Code: ping.Success, RTT: c.rtt}
			if got := Glyph(res, c.scale, c.logK); got != c.want {
				t.Errorf("Glyph(RTT %v, scale %v, logK %d) = %q, want %q",
					c.rtt, c.scale, c.logK, got, c.want)
			}
		})
	}
}

// TestRttGlyphLogBoundaryStability is the regression guard for boundaryEpsilon:
// across non-unit floors and both factors, an RTT exactly on band boundary i
// (floor*e^(logK*i)) must land in band i (rttBars[i]) for i in 0..len(rttBars)-1 and
// overflow to "█" at i == len(rttBars). Without the epsilon nudge, exact boundaries
// such as floor=0.3,i=2 and floor=2.5,i=4 truncate one band too low.
func TestRttGlyphLogBoundaryStability(t *testing.T) {
	floors := []float64{0.3, 1.0, 2.5, 7.0, 0.001}
	for _, floor := range floors {
		for _, logK := range []int{1, 2} {
			for i := 0; i <= len(rttBars); i++ {
				rtt := floor * math.Exp(float64(logK*i))

				want := "█"
				if i < len(rttBars) {
					want = rttBars[i]
				}

				res := ping.Result{Success: true, Code: ping.Success, RTT: rtt}
				if got := Glyph(res, floor, logK); got != want {
					t.Errorf("floor=%v logK=%d band=%d (rtt=%v): Glyph = %q, want %q",
						floor, logK, i, rtt, got, want)
				}
			}
		}
	}
}

// TestGlyphFailureCodesLogMode confirms log mode does not disturb the failure-code
// mapping: a failed probe is still X (and SSH timeout/failure t/s) whatever the log
// factor, since the switch routes before any RTT bucketing.
func TestGlyphFailureCodesLogMode(t *testing.T) {
	cases := []struct {
		res  ping.Result
		want string
	}{
		{ping.Result{Code: ping.Failed}, "X"},
		{ping.Result{Code: ping.SSHTimeout}, "t"},
		{ping.Result{Code: ping.SSHFailed}, "s"},
	}
	for _, c := range cases {
		if got := Glyph(c.res, 1.0, 1); got != c.want {
			t.Errorf("Glyph(%+v, logK=1) = %q, want %q", c.res, got, c.want)
		}
	}
}
