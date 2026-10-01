package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/domain/monitor"
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
	samples := []monitor.Stats{
		{
			LossRate: 5,
			RTT:      12,
			Avg:      14,
			Min:      9,
			Max:      35,
			Jit:      2,
			Snt:      100,
			Loss:     3,
		},
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

// A long-uptime SNT/FAIL count (>=10000) overflows the fixed 5-wide stat header. In the
// single-column layout (no padCell safety net) that previously pushed the row past the
// terminal width, so Bubble Tea clipped the oldest result glyph and bar-starts went
// ragged. After the dynamic stat-width sizing, a layout recompute keeps every row at or
// under the terminal width.
func TestSingleColumnWideCountKeepsRowWidth(t *testing.T) {
	const width = 120

	m := newModel(t, manySpecs(3), testOptions{Scale: 10})

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

	// Fill history so the bar is full, then force 6-digit SNT/FAIL.
	fillWideCounts(m)

	// A layout recompute (a resize here; in the app, every probe result) picks up the
	// widened stat columns and shrinks the result bar to keep the row aligned.
	_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})
	assertNoLineExceedsWidth(t, out, width)
}
