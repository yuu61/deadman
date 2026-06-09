package tui

import (
	"fmt"
	"strings"

	"github.com/yuu61/deadman/internal/monitor"
)

// statColWidth is the display width every statistics column renders to (header
// label and cell alike), so a header always sits directly over its values.
const statColWidth = 5

// statColumn is one statistics column: a fixed-width header label and a cell
// formatter. Header and the string Cell returns must each be statColWidth wide.
type statColumn struct {
	Key    string
	Header string
	Cell   func(t *monitor.Target) string
}

// col4f / col4i format a value into the shared " %4d" cell (floats are truncated
// to whole milliseconds, matching RTT/AVG/MIN/MAX/JIT).
func col4f(v float64) string { return fmt.Sprintf(" %4d", int(v)) }
func col4i(v int) string     { return fmt.Sprintf(" %4d", v) }

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
	{Key: colRTT, Header: "  RTT", Cell: func(t *monitor.Target) string { return col4f(t.RTT) }},
	{Key: colAvg, Header: "  AVG", Cell: func(t *monitor.Target) string { return col4f(t.Avg) }},
	{Key: colMin, Header: "  MIN", Cell: func(t *monitor.Target) string { return col4f(t.Min) }},
	{Key: colMax, Header: "  MAX", Cell: func(t *monitor.Target) string { return col4f(t.Max) }},
	{Key: colJit, Header: "  JIT", Cell: func(t *monitor.Target) string { return col4f(t.Jit) }},
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

// statsHeader builds the statistics header from the visible columns. It also
// drives the width math in recalcWidths, so the header and each row stay aligned.
func (m Model) statsHeader() string {
	var b strings.Builder

	for _, c := range statColumns {
		if m.columnVisible(c.Key) {
			b.WriteString(c.Header)
		}
	}

	return b.String()
}

// statsLine formats one target's visible numeric columns to match statsHeader,
// plus the two-space gap before the RESULT bar.
func (m Model) statsLine(t *monitor.Target) string {
	var b strings.Builder

	for _, c := range statColumns {
		if m.columnVisible(c.Key) {
			b.WriteString(c.Cell(t))
		}
	}

	b.WriteString("  ")

	return b.String()
}
