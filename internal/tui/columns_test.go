package tui

import (
	"testing"

	"github.com/yuu61/deadman/internal/monitor"
)

// In every precision mode, each column's header and formatted cell must be the same
// display width, or the header stops sitting directly over its values. Time-stat
// columns take the mode's width; the fixed columns (LOSS/SNT/FAIL) stay 5 wide in
// every mode.
func TestStatColumnWidths(t *testing.T) {
	// Two samples: ordinary sub-100ms values, and high-latency values up to 9999ms
	// (common on WAN/LTE). Every mode reserves four integer digits, so both must fit
	// without overflowing the declared width. Keep all values <= 9999: a five-digit
	// value would (correctly) overflow even the fixed widths.
	samples := []*monitor.Target{
		{LossRate: 5, RTT: 12, Avg: 14, Min: 9, Max: 35, Jit: 2, Snt: 100, Loss: 3},
		{LossRate: 1, RTT: 1234, Avg: 987, Min: 123, Max: 9876, Jit: 45, Snt: 10, Loss: 0},
	}

	for _, mode := range precisionModes {
		for _, c := range statColumns {
			want := mode.Width
			if c.Value == nil {
				want = msWidth // fixed columns are outside the precision axis.
			}

			if w := displayWidth(c.header(mode)); w != want {
				t.Errorf("mode %s column %s header %q width = %d, want %d",
					mode.Label, c.Key, c.header(mode), w, want)
			}

			for _, sample := range samples {
				if cell := c.cell(sample, mode); displayWidth(cell) != want {
					t.Errorf("mode %s column %s cell %q width = %d, want %d",
						mode.Label, c.Key, cell, displayWidth(cell), want)
				}
			}
		}
	}
}

// buildVisible seeds every column shown, applies known overrides, and ignores
// unknown keys.
func TestBuildVisible(t *testing.T) {
	v := buildVisible(map[string]bool{
		"MIN": false, "BOGUS": false, "VIA": false, "HOSTNAME": false,
	})

	if !v["LOSS"] || !v["MAX"] {
		t.Errorf("unspecified columns should stay shown: %+v", v)
	}

	if v["MIN"] {
		t.Errorf("MIN override not applied: %+v", v)
	}

	// The structural VIA/HOSTNAME columns are seeded too, so their overrides apply,
	// while the unspecified ADDRESS column keeps its shown default.
	if v["VIA"] {
		t.Errorf("VIA override not applied: %+v", v)
	}

	if v["HOSTNAME"] {
		t.Errorf("HOSTNAME override not applied: %+v", v)
	}

	if !v["ADDRESS"] {
		t.Errorf("unspecified ADDRESS should stay shown: %+v", v)
	}

	if _, ok := v["BOGUS"]; ok {
		t.Errorf("unknown column should be ignored: %+v", v)
	}
}
