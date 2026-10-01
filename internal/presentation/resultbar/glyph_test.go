package resultbar

import (
	"math"
	"testing"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestGlyph(t *testing.T) {
	const scale = 10

	cases := []struct {
		name string
		res  probe.Result
		want string
	}{
		{"sub_scale", probe.Result{Code: probe.Success, RTT: 9}, "▁"},
		{"one_scale", probe.Result{Code: probe.Success, RTT: 10}, "▂"},
		{"near_seven", probe.Result{Code: probe.Success, RTT: 65}, "▇"},
		{"at_seven", probe.Result{Code: probe.Success, RTT: 70}, "█"},
		{"over", probe.Result{Code: probe.Success, RTT: 1000}, "█"},
		{"failed", probe.Result{Code: probe.Failed}, "X"},
		{"relay_timeout", probe.Result{Code: probe.RelayTimeout}, "t"},
		{"relay_failed", probe.Result{Code: probe.RelayFailed}, "s"},
		{"unavailable", probe.Result{Code: probe.Unavailable}, "?"},
		{"unknown_code", probe.Result{Code: probe.ResultCode(99)}, "X"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Glyph(c.res, scale, 0, BarBlock); got != c.want {
				t.Errorf("Glyph(%+v) = %q, want %q", c.res, got, c.want)
			}
		})
	}
}

// TestResultsRescale shows the result bar re-buckets when the scale changes: the
// same stored result renders to a different glyph at a different scale. This is what
// lets the up/down keys re-scale bars already on screen.
func TestResultsRescale(t *testing.T) {
	tg := &monitor.Target{}
	tg.Consume(probe.Result{Code: probe.Success, RTT: 15})

	res := tg.Snapshot().At(0)
	// RTT 15: at scale 10 it lands in the 2nd bucket (10..20 -> ▂); at scale 5 it is
	// in the 4th (15 == 5*3, < 5*4 -> ▄).
	if got := Glyph(res, 10, 0, BarBlock); got != "▂" {
		t.Errorf("Glyph(RTT 15, scale 10) = %q, want ▂", got)
	}

	if got := Glyph(res, 5, 0, BarBlock); got != "▄" {
		t.Errorf("Glyph(RTT 15, scale 5) = %q, want ▄", got)
	}
}

// TestRttGlyphLinearBoundary is the regression guard for boundaryEpsilon: with a
// fractional linear scale, an RTT exactly on a band boundary like rtt=0.3, scale=0.1
// (where 0.1*3 rounds to 0.30000000000000004 and rtt/scale to 2.9999999999999996)
// must land in the band it opens (▄), not the one below (▃). Removing boundaryEpsilon
// fails these — unlike the log boundary tests, whose floors hit exact integer steps.
func TestRttGlyphLinearBoundary(t *testing.T) {
	cases := []struct {
		rtt, scale float64
		want       string
	}{
		{0.3, 0.1, "▄"}, // 0.1*3 = 0.30000000000000004; 0.3 opens band 3.
		{0.7, 0.1, "█"}, // 0.1*7 = 0.7000000000000001; 0.7 overflows the last band.
		{0.2, 0.1, "▃"}, // band 2, no boundary artifact.
		{0.05, 0.05, "▂"},
	}
	for _, c := range cases {
		res := probe.Result{Code: probe.Success, RTT: c.rtt}
		if got := Glyph(res, c.scale, 0, BarBlock); got != c.want {
			t.Errorf("Glyph(RTT %v, scale %v, linear) = %q, want %q", c.rtt, c.scale, got, c.want)
		}
	}
}

// TestRttGlyphLog covers the log-mode cases that TestRttGlyphLogBoundaryStability's
// exhaustive boundary sweep does not: an RTT below the floor, a mid-band value (not on a
// boundary), a far overflow, the zero/negative guards, and the lnBase==0 linear
// passthrough. The exact band boundaries (floor*e^(lnBase*i) → band i) are pinned by
// the sweep across several floors and both factors, so they are not duplicated here.
func TestRttGlyphLog(t *testing.T) {
	exp := math.Exp

	cases := []struct {
		name   string
		rtt    float64
		scale  float64
		lnBase float64
		want   string
	}{
		// Below the floor (step < 0 in log space) clamps to the lowest bar.
		{"below_floor", 0.5, 1.0, 1, "▁"},
		// Mid-band (not on a boundary) lands in the lower band: floor(2.5) = band 2.
		{"mid_band_2_3", exp(2.5), 1.0, 1, "▃"},
		// Far past the last boundary still overflows to the full block.
		{"far_overflow", 1e9, 1.0, 1, "█"},

		// Guards: zero/negative RTT and a degenerate floor fall back to ▁.
		{"zero_rtt", 0.0, 1.0, 1, "▁"},
		{"neg_floor", exp(3), -1.0, 1, "▁"},

		// lnBase=0 stays linear even through this table.
		{"linear_9_scale10", 9.0, 10.0, 0, "▁"},
		{"linear_70_scale10", 70.0, 10.0, 0, "█"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := probe.Result{Code: probe.Success, RTT: c.rtt}
			if got := Glyph(res, c.scale, c.lnBase, BarBlock); got != c.want {
				t.Errorf("Glyph(RTT %v, scale %v, lnBase %g) = %q, want %q",
					c.rtt, c.scale, c.lnBase, got, c.want)
			}
		})
	}
}

// TestRttGlyphLogBoundaryStability checks the log boundary contract across non-unit
// floors and both factors: an RTT exactly on band boundary i (floor*e^(lnBase*i)) lands
// in band i (the i-th block glyph) for every band and overflows to "█" one past the
// last band. These particular floors land on exact integer steps (so the
// boundaryEpsilon regression itself is guarded by TestRttGlyphLinearBoundary, where
// float rounding actually bites), but they pin the band semantics.
func TestRttGlyphLogBoundaryStability(t *testing.T) {
	blockBars := BarBlock.glyphs()
	bands := len(blockBars) - 1

	floors := []float64{0.3, 1.0, 2.5, 7.0, 0.001}
	for _, floor := range floors {
		for _, lnBase := range []float64{1, 2} {
			for i := 0; i <= bands; i++ {
				rtt := floor * math.Exp(lnBase*float64(i))

				want := "█"
				if i < bands {
					want = blockBars[i]
				}

				res := probe.Result{Code: probe.Success, RTT: rtt}
				if got := Glyph(res, floor, lnBase, BarBlock); got != want {
					t.Errorf("floor=%v lnBase=%g band=%d (rtt=%v): Glyph = %q, want %q",
						floor, lnBase, i, rtt, got, want)
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
		res  probe.Result
		want string
	}{
		{probe.Result{Code: probe.Failed}, "X"},
		{probe.Result{Code: probe.RelayTimeout}, "t"},
		{probe.Result{Code: probe.RelayFailed}, "s"},
	}
	for _, c := range cases {
		if got := Glyph(c.res, 1.0, 1, BarBlock); got != c.want {
			t.Errorf("Glyph(%+v, lnBase=1) = %q, want %q", c.res, got, c.want)
		}
	}
}
