package tui

import "slices"

// layout is what the terminal size and the rows make of the preferences: the column
// widths, recomputed by recalcWidths, and the scroll position.
type layout struct {
	width, height int

	hostW, addrW, viaW, resW int

	// statW is the rendered width of each stat column (keyed by column key), sized to
	// the widest header/cell across targets so a long-uptime SNT/FAIL count (or a
	// >=10000 ms stat) widens its column instead of overflowing the fixed header and
	// shifting the result bar. Recomputed in recalcWidths.
	statW map[string]int

	scrollTop int // first visible row when the list exceeds the viewport; moved with j/k/g/G/PgUp/PgDn.
}

// recalcWidths recomputes the dynamic column widths and returns the updated model,
// keeping every Model method a value receiver.
func (m Model) recalcWidths() Model {
	// Seed the floors with a trailing space (len("HOSTNAME ") = 9,
	// len("ADDRESS ") = 8).
	hlen := len("HOSTNAME ")

	for _, r := range m.rows() {
		hlen = max(hlen, displayWidth(rowIdentity(r).name))
	}

	if hlen > maxHostnameLength {
		hlen = maxHostnameLength
	}

	m.hostW = hlen

	alen := len("ADDRESS ")

	for _, r := range m.rows() {
		alen = max(alen, displayWidth(rowIdentity(r).addr))
	}

	if alen > maxAddressLength {
		alen = maxAddressLength
	} else {
		alen += 5
	}

	m.addrW = alen

	m.viaW = m.viaWidth()

	// Size the stat columns before the fixed width: rowFixedWidth measures statsHeader,
	// which now pads each stat column to statW.
	m.statW = m.computeStatWidths()

	// The terminal width is split across effectiveCols newspaper columns; resW
	// absorbs the leftover within one column's content width. With a single column
	// colContentWidth == m.width, so this matches the original full-width bar exactly.
	used := m.rowFixedWidth()
	m.resW = max(m.colContentWidth()-used, minResultWidth)

	return m
}

// rowFixedWidth is the display width of one row's fixed part — everything left of
// the result bar: the arrow, HOSTNAME, ADDRESS, the optional VIA column, the stats
// columns, and the two-space gap. The result bar fills whatever column width
// remains. recalcWidths (to size the bar) and minColumnWidth (to size a column) both
// derive from this, so the two can never drift.
func (m Model) rowFixedWidth() int {
	// arrow [+ host + 1] [+ addr + 1] [+ via + 1] + statsHeader + 2. The optional
	// segments gate on the SAME columnVisible calls headerLine/targetLine use, so the
	// measured fixed width and the rendered row can never drift (a drift would mis-size
	// the result bar).
	used := len(arrow) + len(m.statsHeader()) + 2
	if m.columnVisible(colHost) {
		used += m.hostW + 1
	}

	if m.columnVisible(colAddr) {
		used += m.addrW + 1
	}

	if m.columnVisible(colVia) {
		used += m.viaW + 1
	}

	return used
}

// minColumnWidth is the narrowest a single newspaper column may be: a full row plus
// the minimum result bar. effectiveCols never returns a count that forces a column
// below this, so a column can never overflow its slice of the width.
func (m Model) minColumnWidth() int {
	return m.rowFixedWidth() + minResultWidth
}

// usableRows is the number of screen rows available for the scrollable list:
// the terminal height minus the fixed header. Zero before the first size message
// or when the header alone fills the screen.
func (m Model) usableRows() int {
	if m.height <= 0 {
		return 0
	}

	return max(m.height-m.fixedHeaderLines(), 0)
}

// effectiveCols clamps the requested column count (m.cols) to what actually fits.
// First by width: n columns need n*minCol + (n-1)*gutter <= width, i.e.
// n <= (width+gutter)/(minCol+gutter). Then by row count: when the list fits the
// height, only ceil(rows/perColumn) columns are needed to hold every row, so a
// larger request would leave a phantom header-only column on the right. (When the
// list overflows and scrolls, every column is full, so no row reduction applies.)
func (m Model) effectiveCols() int {
	// An empty row set (a config of only directives/comments, or a reload to a
	// temporarily empty config) trivially fits one column. Returning here keeps the
	// width/request fit from leaving header-only phantom columns the row-count clamp
	// below would otherwise prevent (it is guarded on usableRows, which the clamp
	// also needs).
	if m.cols <= 1 || m.width <= 0 || len(m.rows()) == 0 {
		return 1
	}

	fit := (m.width + colGutterWidth) / (m.minColumnWidth() + colGutterWidth)
	eff := max(1, min(m.cols, fit))

	// When the whole list fits the height, only ceil(rows/perColumn) columns hold
	// every row; a larger fit would leave a phantom header-only column. len(m.rows()) is
	// >= 1 here (the empty case returned above).
	if usable := m.usableRows(); usable > 0 && len(m.rows()) <= usable*eff {
		perCol := ceilDiv(len(m.rows()), eff)
		eff = ceilDiv(len(m.rows()), perCol)
	}

	return eff
}

// colContentWidth is the per-column content width: the terminal width minus the
// inter-column gutters, divided across the effective columns. With one column it is
// exactly m.width (no gutters). The floor division pairs with effectiveCols so the
// result is always >= minColumnWidth, keeping resW >= minResultWidth (no overflow).
func (m Model) colContentWidth() int {
	eff := m.effectiveCols()

	return (m.width - (eff-1)*colGutterWidth) / eff
}

// viaWidth is the display width of the VIA column: the widest target label,
// floored by the "VIA" header and capped at maxViaLength.
func (m Model) viaWidth() int {
	w := len("VIA")

	for _, label := range m.labels {
		w = max(w, displayWidth(label))
	}

	if w > maxViaLength {
		w = maxViaLength
	}

	return w
}

// viewport describes which slice of rows View renders. When the list fits the
// screen active is false and every row is shown; otherwise the window is
// rows[top : top+count] and, when there is room (status), a one-line scroll
// indicator is appended below it. cols/perCol carry the newspaper-grid shape: the
// window is laid out column-major into cols columns of perCol rows each, so
// count == perCol*cols (cols is 1 in the single-column layout).
type viewport struct {
	top, count, maxTop int
	cols, perCol       int
	active, status     bool
}

// ceilDiv returns ceil(a/b) for non-negative a and positive b, used to size a
// column-major grid's height from the row count and the column count.
func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}

	return (a + b - 1) / b
}

// scrollMetrics derives the visible row window from the terminal height. It is
// the single source of truth shared by View (to slice rows) and the scroll keys
// (to clamp scrollTop), so the rendered window and the clamp can never disagree.
// Before the first WindowSizeMsg (width/height still 0) the whole list is shown.
func (m Model) scrollMetrics() viewport {
	if m.width == 0 || m.height <= 0 {
		return viewport{count: len(m.rows()), cols: 1, perCol: len(m.rows())}
	}

	eff := m.effectiveCols()

	// usable is 0 when the fixed header fills (or exceeds) the screen. In that case
	// render no rows: forcing a minimum of 1 here would emit height+1 lines, and
	// Bubble Tea drops the top (title) line, defeating the fixed-header guarantee.
	usable := m.usableRows()

	// The grid holds usable rows per column across eff columns. When the whole list
	// fits, show it all unscrolled; perCol is the column-major column height (column
	// 0 fills first) and is <= usable, so the merged block never overflows the screen.
	if len(m.rows()) <= usable*eff {
		return viewport{count: len(m.rows()), cols: eff, perCol: ceilDiv(len(m.rows()), eff)}
	}

	// Reserve the bottom usable line for the scroll indicator, unless that would
	// leave no room for a data row (usable <= 1). At usable == 1 (a terminal only
	// one row taller than the header) we deliberately spend that row on data rather
	// than the indicator, so the list still scrolls but shows no position hint.
	status := usable >= 2

	perCol := usable
	if status {
		perCol = usable - 1
	}

	count := perCol * eff
	maxTop := len(m.rows()) - count
	top := max(min(m.scrollTop, maxTop), 0)

	return viewport{
		top:    top,
		count:  count,
		maxTop: maxTop,
		cols:   eff,
		perCol: perCol,
		active: true,
		status: status,
	}
}

// fixedHeaderLines is the number of screen rows View renders above the scrollable
// window: the centered title, the host-info line, the keys line, one row per
// startup warning, a blank separator, and the column header. Bubble Tea's renderer
// truncates over-wide lines to the terminal width rather than wrapping them (see
// standard_renderer flush), so each is exactly one row and this logical count is
// exact.
func (m Model) fixedHeaderLines() int {
	return nonWarnLines + len(m.headerWarnings())
}

// nonWarnLines are the fixed header lines besides the warnings: the title, the host info,
// the keys, the blank separator and the column header.
const nonWarnLines = 5

// headerWarnings is the warnings as the header shows them. They may take at most half of
// the rows the other header lines leave, so no number of them can push the targets off
// the screen; past that, the last line shown says how many more there are.
func (m Model) headerWarnings() []string {
	limit := max((m.height-nonWarnLines)/2, 1)
	if m.height <= 0 || len(m.warnings) <= limit {
		return m.warnings
	}

	shown := slices.Clone(m.warnings[:limit-1])

	return append(shown, overflowLine(len(m.warnings)-len(shown)))
}

// clampScroll re-pins scrollTop into the current valid range, used after a resize
// or a reload that shrinks the target set. It preserves the position while the list
// still overflows; growing the terminal until everything fits naturally lands at the
// top (scrollMetrics returns top 0 when no scrolling is needed).
func (m Model) clampScroll() Model {
	m.scrollTop = m.scrollMetrics().top

	return m
}
