package tui

import (
	"fmt"
	"strings"

	"github.com/yuu61/deadman/internal/monitor"
)

// precisionMode is one display precision for the time-stat columns
// (RTT/AVG/MIN/MAX/JIT): a footer/config label, the display width every such column
// renders to in this mode (header and cell alike), and the cell formatter. Every mode
// reports in milliseconds; the label's ".N" suffix is the number of decimal places,
// trading column width for sub-millisecond resolution (ms.3 resolves microseconds).
// precisionModes is the single source of both the 'p'-key cycle order and the accepted
// config "precision" directive values, so adding a mode (ms.4, …) is one entry here.
// Cells and headers stay ASCII so the column-width math holds.
type precisionMode struct {
	Label  string
	Width  int
	Format func(v float64) string
}

// Stat-cell widths per precision mode: the header label and each cell render to this
// many columns. Each added decimal place is one more column (a leading space, the
// integer part, the dot, and N decimals).
const (
	msWidth  = 5 // integer ms, e.g. " 1234".
	ms1Width = 6 // one decimal, e.g. " 123.4".
	ms2Width = 7 // two decimals, e.g. " 123.45".
	ms3Width = 8 // three decimals (µs resolution), e.g. " 123.456".
)

var precisionModes = []precisionMode{
	{
		Label:  "ms",
		Width:  msWidth,
		Format: func(v float64) string { return fmt.Sprintf(" %4d", int(v)) },
	},
	{
		Label:  "ms.1",
		Width:  ms1Width,
		Format: func(v float64) string { return fmt.Sprintf(" %5.1f", v) },
	},
	{
		Label:  "ms.2",
		Width:  ms2Width,
		Format: func(v float64) string { return fmt.Sprintf(" %6.2f", v) },
	},
	{
		Label:  "ms.3",
		Width:  ms3Width,
		Format: func(v float64) string { return fmt.Sprintf(" %7.3f", v) },
	},
}

// precisionIndex maps a config/precision label to its index in precisionModes,
// defaulting to 0 (ms) for an empty or unknown label.
func precisionIndex(label string) int {
	for i, mode := range precisionModes {
		if mode.Label == label {
			return i
		}
	}

	return 0
}

// statColumn is one statistics column. Time-stat columns set Name+Value and render
// at the active precision mode's width (header and cell alike); the fixed columns
// (LOSS/SNT/FAIL) set Header+Cell and render to a fixed 5 columns, outside the
// precision axis.
type statColumn struct {
	Key string

	Name  string                          // time-stat columns: header label, right-aligned to the mode width.
	Value func(t *monitor.Target) float64 // time-stat columns: the raw ms value to format.

	Header string                         // fixed columns: the 5-wide header label.
	Cell   func(t *monitor.Target) string // fixed columns: the 5-wide cell.
}

// header renders the column's header for the active precision mode: a time-stat
// column right-aligns its Name to the mode width; a fixed column uses its Header.
func (c statColumn) header(mode precisionMode) string {
	if c.Value != nil {
		return fmt.Sprintf("%*s", mode.Width, c.Name)
	}

	return c.Header
}

// cell renders one target's value for the active precision mode.
func (c statColumn) cell(t *monitor.Target, mode precisionMode) string {
	if c.Value != nil {
		return mode.Format(c.Value(t))
	}

	return c.Cell(t)
}

// col4i formats a count into the shared 5-wide " %4d" cell (SNT/FAIL).
func col4i(v int) string { return fmt.Sprintf(" %4d", v) }

// Column keys. These double as the names accepted in the config "columns"
// directive (matched case-insensitively there).
const (
	colLoss = "LOSS"
	colRTT  = "RTT"
	colAvg  = "AVG"
	colMin  = "MIN"
	colMax  = "MAX"
	colJit  = "JIT"
	colSnt  = "SNT"
	colFail = "FAIL"

	// colVia is the structural VIA column (the probing method). Unlike the
	// statColumns it is a variable-width string column rendered in view.go, but it
	// shares the same visibility map so the "columns" directive and the 'v' key can
	// hide it.
	colVia = "VIA"
)

// statColumns is the ordered registry of statistics columns. The header row, each
// target row, and the width math in recalcWidths all derive from this single
// list, so adding a column is one entry here (and it is hideable for free, keyed
// by Key). HOSTNAME/ADDRESS/RESULT are structural columns and live in view.go.
var statColumns = []statColumn{
	{
		Key:    colLoss,
		Header: " LOSS",
		Cell:   func(t *monitor.Target) string { return fmt.Sprintf(" %3d%%", int(t.LossRate)) },
	},
	{Key: colRTT, Name: colRTT, Value: func(t *monitor.Target) float64 { return t.RTT }},
	{Key: colAvg, Name: colAvg, Value: func(t *monitor.Target) float64 { return t.Avg }},
	{Key: colMin, Name: colMin, Value: func(t *monitor.Target) float64 { return t.Min }},
	{Key: colMax, Name: colMax, Value: func(t *monitor.Target) float64 { return t.Max }},
	{Key: colJit, Name: colJit, Value: func(t *monitor.Target) float64 { return t.Jit }},
	{Key: colSnt, Header: "  SNT", Cell: func(t *monitor.Target) string { return col4i(t.Snt) }},
	{Key: colFail, Header: " FAIL", Cell: func(t *monitor.Target) string { return col4i(t.Loss) }},
}

// buildVisible returns a per-column visibility map seeded to all-shown, then
// applies the config overrides (keys not in the registry are ignored). The
// structural VIA column is seeded alongside the statColumns so it is hideable via
// the "columns" directive and the 'v' key.
func buildVisible(overrides map[string]bool) map[string]bool {
	v := make(map[string]bool, len(statColumns)+1)
	for _, c := range statColumns {
		v[c.Key] = true
	}

	v[colVia] = true

	for k, show := range overrides {
		if _, ok := v[k]; ok {
			v[k] = show
		}
	}

	return v
}

// columnVisible reports whether the column is shown. A nil map (a Model not built
// through New) is treated as all-shown.
func (m Model) columnVisible(key string) bool {
	if m.visible == nil {
		return true
	}

	return m.visible[key]
}

// precMode returns the active precision mode for the time-stat columns. A precIdx
// out of range (e.g. a Model not built through New) falls back to ms.
func (m Model) precMode() precisionMode {
	if m.precIdx < 0 || m.precIdx >= len(precisionModes) {
		return precisionModes[0]
	}

	return precisionModes[m.precIdx]
}

// statsHeader builds the statistics header from the visible columns. It also
// drives the width math in recalcWidths, so the header and each row stay aligned.
func (m Model) statsHeader() string {
	mode := m.precMode()

	var b strings.Builder

	for _, c := range statColumns {
		if m.columnVisible(c.Key) {
			b.WriteString(c.header(mode))
		}
	}

	return b.String()
}

// statsLine formats one target's visible numeric columns to match statsHeader,
// plus the two-space gap before the RESULT bar.
func (m Model) statsLine(t *monitor.Target) string {
	mode := m.precMode()

	var b strings.Builder

	for _, c := range statColumns {
		if m.columnVisible(c.Key) {
			b.WriteString(c.cell(t, mode))
		}
	}

	b.WriteString("  ")

	return b.String()
}
