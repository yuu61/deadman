package tui

import (
	"testing"

	"github.com/yuu61/deadman/internal/monitor"
)

// Every column's header and formatted cell must be exactly statColWidth display
// columns wide, or the header stops sitting directly over its values.
func TestStatColumnWidths(t *testing.T) {
	sample := &monitor.Target{
		LossRate: 5, RTT: 12, Avg: 14, Min: 9, Max: 35, Jit: 2, Snt: 100, Loss: 3,
	}

	for _, c := range statColumns {
		if w := displayWidth(c.Header); w != statColWidth {
			t.Errorf("column %s header %q width = %d, want %d", c.Key, c.Header, w, statColWidth)
		}

		if cell := c.Cell(sample); displayWidth(cell) != statColWidth {
			t.Errorf(
				"column %s cell %q width = %d, want %d",
				c.Key,
				cell,
				displayWidth(cell),
				statColWidth,
			)
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
