package resultbar

import (
	"math"
	"testing"
)

// TestValidScale pins the usable-scale window: the [MinScale, MaxScale] bounds (and the
// implicit rejection of zero, negatives, NaN and ±Inf). Extreme finite values are
// rejected too — below MinScale the footer label balloons and every bar overflows.
func TestValidScale(t *testing.T) {
	cases := []struct {
		v  float64
		ok bool
	}{
		{10, true},
		{0.01, true},     // sub-ms ladder bottom.
		{MinScale, true}, // inclusive lower bound.
		{MaxScale, true}, // inclusive upper bound.
		{0, false},
		{-5, false},
		{1e-300, false}, // far below MinScale: ~300-char footer label, bar all █.
		{1e300, false},  // far above MaxScale.
		{math.Inf(1), false},
		{math.Inf(-1), false},
		{math.NaN(), false},
	}
	for _, c := range cases {
		if got := ValidScale(c.v); got != c.ok {
			t.Errorf("ValidScale(%g) = %v, want %v", c.v, got, c.ok)
		}
	}
}

// TestScaleOrDefault confirms the shared "invalid → default" normalization: a usable
// value passes through, anything ValidScale rejects becomes DefaultScale.
func TestScaleOrDefault(t *testing.T) {
	cases := []struct {
		v, want float64
	}{
		{5, 5},
		{0.5, 0.5},
		{0, DefaultScale},
		{-1, DefaultScale},
		{1e-300, DefaultScale},
		{math.Inf(1), DefaultScale},
		{math.NaN(), DefaultScale},
	}
	for _, c := range cases {
		if got := ScaleOrDefault(c.v); got != c.want {
			t.Errorf("ScaleOrDefault(%g) = %g, want %g", c.v, got, c.want)
		}
	}
}
