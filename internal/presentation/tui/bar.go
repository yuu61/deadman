package tui

import (
	"strings"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// resultBar styles adjacent cells of the same level together. Styling every cell
// separately repeats the color and reset escapes hundreds of times per row; a
// console can display intermediate frames as that output arrives in pty chunks.
// Runs retain each result's glyph, including the distinct failure codes, and end
// with a reset so a red failure background cannot leak into the next column.
func (m Model) resultBar(t monitor.Snapshot) string {
	var out, run strings.Builder

	lnBase := m.logFactor().LnBase
	n := min(t.Len(), m.resW)

	for i := 0; i < n; {
		level := resultbar.Level(t.At(i), m.scale, lnBase, m.bar)

		run.Reset()

		for i < n && resultbar.Level(t.At(i), m.scale, lnBase, m.bar) == level {
			run.WriteString(resultbar.Glyph(t.At(i), m.scale, lnBase, m.bar))
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
