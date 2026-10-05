package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// resultBar styles adjacent cells of the same level together. Styling every cell
// separately repeats the color and reset escapes hundreds of times per row; a
// console can display intermediate frames as that output arrives in pty chunks.
// Runs retain each result's glyph, including the distinct failure codes, and end
// with a reset so a red failure background cannot leak into the next column.
func (m Model) resultBar(snapshot monitor.Snapshot) string {
	lnBase := m.logFactor().LnBase

	n := min(snapshot.Len(), m.resW)
	if n <= 0 {
		return ""
	}

	if lipgloss.ColorProfile() == termenv.Ascii {
		return m.plainResultBar(snapshot, n, lnBase)
	}

	var out, run strings.Builder

	for i := 0; i < n; {
		level := resultbar.Level(snapshot.At(i), m.scale, lnBase, m.bar)

		run.Reset()

		for i < n && resultbar.Level(snapshot.At(i), m.scale, lnBase, m.bar) == level {
			// A success's glyph shares the run's level; do not bucket its RTT again.
			glyph := m.bar.GlyphAt(level)
			if level == resultbar.NoLevel {
				glyph = resultbar.Glyph(snapshot.At(i), m.scale, lnBase, m.bar)
			}

			run.WriteString(glyph)

			i++
		}

		style := styleFail
		if level != resultbar.NoLevel {
			style = rttStyle(m.bar, level)
		}

		out.WriteString(style.Render(run.String()))
	}

	return out.String()
}

// The bar styles only add color, so a colorless terminal needs neither runs nor Render.
func (m Model) plainResultBar(t monitor.Snapshot, n int, lnBase float64) string {
	var out strings.Builder
	out.Grow(n * len(m.bar.GlyphAt(0)))

	for i := range n {
		out.WriteString(resultbar.Glyph(t.At(i), m.scale, lnBase, m.bar))
	}

	return out.String()
}
