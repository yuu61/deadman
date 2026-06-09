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
	sample := &monitor.Target{
		LossRate: 5, RTT: 12, Avg: 14, Min: 9, Max: 35, Jit: 2, Snt: 100, Loss: 3,
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

			if cell := c.cell(sample, mode); displayWidth(cell) != want {
				t.Errorf("mode %s column %s cell %q width = %d, want %d",
					mode.Label, c.Key, cell, displayWidth(cell), want)
			}
		}
	}
}

// buildVisible seeds every column shown, applies known overrides, and ignores
// unknown keys.
func TestBuildVisible(t *testing.T) {
	v := buildVisible(map[string]bool{"MIN": false, "BOGUS": false, "VIA": false})

	if !v["LOSS"] || !v["MAX"] {
		t.Errorf("unspecified columns should stay shown: %+v", v)
	}

	if v["MIN"] {
		t.Errorf("MIN override not applied: %+v", v)
	}

	// The structural VIA column is seeded too, so its override applies.
	if v["VIA"] {
		t.Errorf("VIA override not applied: %+v", v)
	}

	if _, ok := v["BOGUS"]; ok {
		t.Errorf("unknown column should be ignored: %+v", v)
	}
}
