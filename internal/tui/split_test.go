package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

// sizedModel builds a model from manySpecs(n) with the given options and feeds one
// WindowSizeMsg so the width/height-derived layout is computed.
func sizedModel(t *testing.T, n int, opts Options, w, h int) Model {
	t.Helper()

	m, err := New(manySpecs(n), opts)
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: w, Height: h})

	return m
}

// effectiveCols clamps the requested count to what the terminal width fits. Widths
// are derived from the model's own minColumnWidth so the test does not hard-code the
// (column-visibility-dependent) per-column budget.
func TestEffectiveColsAndWidthMath(t *testing.T) {
	// minColumnWidth is width-independent, so any base model gives the real budget.
	base := sizedModel(t, 12, Options{Scale: 10, Cols: 1}, 300, 40)
	mw := base.minColumnWidth()
	g := colGutterWidth

	// widthFor is the exact terminal width that fits k columns plus their gutters.
	widthFor := func(k int) int { return k*mw + (k-1)*g }

	cases := []struct {
		name      string
		requested int
		width     int
		wantCols  int
	}{
		{"single column requested fits anywhere", 1, widthFor(3), 1},
		{"two columns fit their exact width", 2, widthFor(2), 2},
		{"one pixel short drops to one column", 2, widthFor(2) - 1, 1},
		{"three columns fit their exact width", 3, widthFor(3), 3},
		{"request beyond fit is clamped", 9, widthFor(2), 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := sizedModel(t, 12, Options{Scale: 10, Cols: c.requested}, c.width, 40)

			if got := m.effectiveCols(); got != c.wantCols {
				t.Errorf(
					"effectiveCols() = %d, want %d (width %d, mw %d)",
					got,
					c.wantCols,
					c.width,
					mw,
				)
			}

			// When the layout actually splits, no column may drop below the minimum
			// (eff==1 is the single-column path, where a narrow terminal legitimately
			// truncates the row as it always has).
			if m.effectiveCols() >= 2 {
				if cw := m.colContentWidth(); cw < m.minColumnWidth() {
					t.Errorf(
						"colContentWidth %d < minColumnWidth %d (column would overflow)",
						cw,
						m.minColumnWidth(),
					)
				}
			}

			if m.resW < minResultWidth {
				t.Errorf("resW %d below floor %d", m.resW, minResultWidth)
			}
		})
	}
}

// Whenever the layout splits (eff >= 2) across a width sweep, the per-column width
// stays at or above the minimum — the no-overflow invariant that lets every cell be
// padded, never truncated.
func TestColContentWidthNeverOverflows(t *testing.T) {
	for _, req := range []int{1, 2, 3, 4} {
		for w := 40; w <= 600; w += 7 {
			m := sizedModel(t, 30, Options{Scale: 10, Cols: req}, w, 40)
			if m.effectiveCols() < 2 {
				continue
			}

			if cw := m.colContentWidth(); cw < m.minColumnWidth() {
				t.Fatalf(
					"req=%d w=%d: colContentWidth %d < minColumnWidth %d",
					req,
					w,
					cw,
					m.minColumnWidth(),
				)
			}
		}
	}
}

// A single-column model must produce byte-identical width metrics to the original
// full-width path (resW absorbs the whole terminal, no gutters).
func TestSingleColumnMatchesFullWidth(t *testing.T) {
	def := sizedModel(t, 12, Options{Scale: 10}, 120, 40)          // Cols unset -> 1.
	one := sizedModel(t, 12, Options{Scale: 10, Cols: 1}, 120, 40) // explicit 1.

	if def.View() != one.View() {
		t.Error("Cols unset and Cols:1 must render identically")
	}

	if def.colContentWidth() != def.width {
		t.Errorf(
			"single column content width %d != terminal width %d",
			def.colContentWidth(),
			def.width,
		)
	}

	if def.resW != max(def.width-def.rowFixedWidth(), minResultWidth) {
		t.Errorf("single column resW %d != original full-width formula", def.resW)
	}
}

// scrollMetrics carries the grid shape: capacity is perCol*cols, the whole list is
// shown unscrolled when it fits across the columns, and a single column reduces to
// the original metrics.
func TestScrollMetricsGridShape(t *testing.T) {
	// Hide MIN/MAX/VIA so two columns comfortably fit a 160-wide terminal.
	opts := func(cols int) Options {
		return Options{
			Scale:   10,
			Cols:    cols,
			Columns: map[string]bool{"MIN": false, "MAX": false, "VIA": false},
		}
	}

	// 12 rows, 2 columns, tall enough: everything fits, no scroll, perCol = ceil(12/2)=6.
	fits := sizedModel(t, 12, opts(2), 160, 40)

	vp := fits.scrollMetrics()
	if vp.active {
		t.Error("12 rows across 2 columns in a tall terminal should fit (active=false)")
	}

	if vp.cols != 2 || vp.perCol != 6 {
		t.Errorf("fits: cols=%d perCol=%d, want 2 and 6", vp.cols, vp.perCol)
	}

	// 60 rows, 2 columns, short terminal: scrolls, count == perCol*cols, maxTop exact.
	scr := sizedModel(t, 60, opts(2), 160, 20)

	vp = scr.scrollMetrics()
	if !vp.active {
		t.Fatal("60 rows in a short terminal should scroll")
	}

	if vp.count != vp.perCol*vp.cols {
		t.Errorf("count %d != perCol*cols (%d*%d)", vp.count, vp.perCol, vp.cols)
	}

	if vp.maxTop != len(scr.rows)-vp.count {
		t.Errorf("maxTop %d != len-count %d", vp.maxTop, len(scr.rows)-vp.count)
	}
}

// In the two-column layout the list flows column-major: the row that starts the
// second column shares a physical line with the first column's top row, and the
// header is repeated once per column.
func TestTwoColumnLayoutColumnMajor(t *testing.T) {
	// Hide stat columns so two columns fit comfortably; 12 rows over a tall screen.
	opts := Options{Scale: 10, Cols: 2, Columns: map[string]bool{"MIN": false, "MAX": false}}
	m := sizedModel(t, 12, opts, 160, 40)

	vp := m.scrollMetrics()
	if vp.cols != 2 {
		t.Fatalf("precondition: want 2 effective columns, got %d", vp.cols)
	}

	out := m.View()

	// Header repeated once per visual column.
	if n := strings.Count(out, "HOSTNAME"); n != 2 {
		t.Errorf("expected the header twice (one per column), got %d\n---\n%s", n, out)
	}

	// Column-major: column 0 holds h000.. and column 1 starts at h0<perCol>, so the
	// first data line shows both h000 and the start-of-column-1 host side by side.
	startCol1 := vp.top + vp.perCol // absolute index of column 1's first row.
	col1Host := manySpecs(12)[startCol1].Name

	first := lineWith(out, "h000")
	if !strings.Contains(first, col1Host) {
		t.Errorf("column-major: line with h000 should also contain %s\n---\n%s", col1Host, out)
	}
}

// The probe arrow lands on the right absolute row even when that row is in the
// second column.
func TestTwoColumnArrowAbsoluteIndex(t *testing.T) {
	opts := Options{Scale: 10, Cols: 2, Columns: map[string]bool{"MIN": false, "MAX": false}}
	m := sizedModel(t, 12, opts, 160, 40)

	vp := m.scrollMetrics()
	if vp.cols != 2 {
		t.Fatalf("precondition: want 2 columns, got %d", vp.cols)
	}

	// Probe a row guaranteed to land in column 1 (index >= perCol).
	probed := vp.perCol + 1
	_, out := drive(t, m, pingStartMsg{idx: probed, gen: 0})

	if n := strings.Count(out, arrow); n != 1 {
		t.Fatalf("sync mode should show exactly one arrow, got %d\n---\n%s", n, out)
	}

	want := manySpecs(12)[probed].Name
	if ln := lineWith(out, arrow); !strings.Contains(ln, want) {
		t.Errorf("arrow should sit on probed row %s, got %q\n---\n%s", want, ln, out)
	}
}

// The multi-column render must never emit more physical lines than the terminal
// height, at any width (including widths where the request is clamped to 1).
func TestTwoColumnFitsTerminalHeight(t *testing.T) {
	m, err := New(manySpecs(60), Options{Scale: 10, Cols: 3})
	if err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{80, 120, 160, 220, 320} {
		for _, height := range []int{5, 10, 20, 40} {
			_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: height})
			if lines := strings.Count(out, "\n") + 1; lines > height {
				t.Errorf(
					"%dx%d cols=3: rendered %d lines (overflow)\n---\n%s",
					width,
					height,
					lines,
					out,
				)
			}
		}
	}
}

// The clamp is surfaced in the keys line: honored shows N/N, a too-narrow terminal
// shows the reduced effective count.
func TestColumnClampHintInKeysLine(t *testing.T) {
	honored := sizedModel(t, 12, Options{Scale: 10, Cols: 2}, 200, 40)
	if out := honored.View(); !strings.Contains(out, "2/2") {
		t.Errorf("honored 2-column request should show 2/2 in the keys line\n---\n%s", out)
	}

	clamped := sizedModel(t, 12, Options{Scale: 10, Cols: 3}, 80, 40)
	if got := clamped.effectiveCols(); got != 1 {
		t.Fatalf("precondition: 80-wide should clamp 3 to 1, got %d", got)
	}

	if out := clamped.View(); !strings.Contains(out, "1/3") {
		t.Errorf("clamped request should show 1/3 in the keys line\n---\n%s", out)
	}

	// The hint must survive the renderer's width truncation (Bubble Tea clips lines,
	// it does not wrap), so it sits ahead of the long key legend. A clamp happens on
	// a narrow terminal, exactly where a tail-appended hint would be cut.
	keys := lineWith(clamped.View(), "RTT Scale")
	if got := runewidth.Truncate(keys, 80, ""); !strings.Contains(got, "1/3") {
		t.Errorf("clamp hint must survive truncation to 80 columns, got %q", got)
	}
}

// The '[' and ']' keys increase/decrease the requested column count, clamped at 1.
func TestColumnKeysAdjustCount(t *testing.T) {
	m := sizedModel(t, 12, Options{Scale: 10}, 240, 40) // wide enough for several columns.

	m = m.handleViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if m.cols != 2 {
		t.Errorf("after ']' cols = %d, want 2", m.cols)
	}

	m = m.handleViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if m.cols != 3 {
		t.Errorf("after ']' x2 cols = %d, want 3", m.cols)
	}

	m = m.handleViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if m.cols != 2 {
		t.Errorf("after '[' cols = %d, want 2", m.cols)
	}

	// '[' floors at 1.
	m = m.handleViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})

	m = m.handleViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if m.cols != 1 {
		t.Errorf("'[' should floor cols at 1, got %d", m.cols)
	}
}

// A separator row inside a column renders as dashes within that column's width.
func TestTwoColumnSeparatorRenders(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "a0", Addr: "1.0.0.1", Relay: map[string]string{}},
		{IsSeparator: true},
		{Name: "a1", Addr: "1.0.0.2", Relay: map[string]string{}},
		{Name: "a2", Addr: "1.0.0.3", Relay: map[string]string{}},
	}

	m, err := New(
		specs,
		Options{Scale: 10, Cols: 2, Columns: map[string]bool{"MIN": false, "MAX": false}},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 160, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)

	if !strings.Contains(out, "----------") {
		t.Errorf("a separator inside a column should render dashes\n---\n%s", out)
	}
}

// padCell fits a styled string to exactly w display columns: it pads when short and
// truncates (ANSI-aware) when long, so a cell can never push the next column over.
func TestPadCellFitsWidth(t *testing.T) {
	short := styleUp.Render("▁▂▃") // 3 glyphs.
	if got := lipgloss.Width(padCell(short, 8)); got != 8 {
		t.Errorf("padCell(short, 8) width = %d, want 8", got)
	}

	// A styled string wider than the cell must be trimmed to exactly w cells.
	long := styleUp.Render("▁▂▃▄▅▆▇█") + styleDown.Render("XXXXXX") // 14 glyphs wide.
	if got := lipgloss.Width(padCell(long, 6)); got != 6 {
		t.Errorf("padCell(long, 6) width = %d, want 6 (must truncate)", got)
	}
}

// A long-uptime SNT/FAIL count widens the stats cell past its fixed header budget;
// combined with a full result bar that overflows the per-column width. padCell must
// truncate so no rendered line exceeds the terminal width (which would make Bubble
// Tea clip the rightmost column).
func TestTwoColumnOverWideCellTruncated(t *testing.T) {
	const width = 160

	opts := Options{Scale: 10, Cols: 2, Columns: map[string]bool{"MIN": false, "MAX": false}}

	m, err := New(manySpecs(12), opts)
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

	// Fill each target's history (so the result bar is full) and force a 6-digit
	// SNT/FAIL, which makes the stats cell wider than its 5-column header budget.
	for _, r := range m.rows {
		if r.Target == nil {
			continue
		}

		for range 300 {
			r.Target.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 5})
		}

		r.Target.Snt = 123456
		r.Target.Loss = 654321
	}

	for ln := range strings.SplitSeq(m.View(), "\n") {
		if w := lipgloss.Width(ln); w > width {
			t.Errorf("rendered line exceeds terminal width: %d > %d\n%q", w, width, ln)
		}
	}
}

// Requesting more columns than there are rows must not open a phantom header-only
// column: the effective count is capped so every column holds at least one row.
func TestColumnsCappedByRowCount(t *testing.T) {
	// 5 rows, 4 columns requested, wide+tall terminal. Without the cap, perCol would
	// be ceil(5/4)=2 and the 4th column would be header-only empty.
	opts := Options{
		Scale:   10,
		Cols:    4,
		Columns: map[string]bool{"MIN": false, "MAX": false, "VIA": false},
	}
	m := sizedModel(t, 5, opts, 320, 40)

	eff := m.effectiveCols()
	if eff >= len(m.rows) {
		t.Errorf("effectiveCols %d should be capped below the row count %d", eff, len(m.rows))
	}

	out := m.View()

	// One header per displayed column, and never more headers than effective columns.
	if n := strings.Count(out, "HOSTNAME"); n != eff {
		t.Errorf("expected %d headers (one per column), got %d\n---\n%s", eff, n, out)
	}

	// Column-major with the cap: perCol = ceil(5/eff); the last visual column still
	// carries a real row (no header-only column).
	vp := m.scrollMetrics()

	lastColStart := vp.top + (vp.cols-1)*vp.perCol
	if lastColStart >= len(m.rows) {
		t.Errorf(
			"last column starts at row %d, past the %d rows (phantom column)",
			lastColStart,
			len(m.rows),
		)
	}
}

// renderColumns must honor vp.top: when the list scrolls, each column starts at
// vp.top + j*perCol, so a regression that drops the top offset is caught.
func TestTwoColumnScrolledColumnMajor(t *testing.T) {
	opts := Options{Scale: 10, Cols: 2, Columns: map[string]bool{"MIN": false, "MAX": false}}
	// Short terminal so 60 rows overflow and the list scrolls.
	m := sizedModel(t, 60, opts, 160, 12)

	vp := m.scrollMetrics()
	if !vp.active || vp.cols != 2 {
		t.Fatalf(
			"precondition: want an active 2-column scroll, got active=%v cols=%d",
			vp.active,
			vp.cols,
		)
	}

	// Scroll to the bottom; top is now non-zero.
	m = m.scroll("G")

	vp = m.scrollMetrics()
	if vp.top == 0 {
		t.Fatal("precondition: expected a non-zero top after G")
	}

	out := m.View()

	// Early rows are scrolled past.
	if strings.Contains(out, "h000 ") {
		t.Errorf("h000 should be scrolled off\n---\n%s", out)
	}

	// Column-major with a top offset: column 0's first row and column 1's first row
	// (top+perCol) share a physical line.
	col0 := manySpecs(60)[vp.top].Name

	col1 := manySpecs(60)[vp.top+vp.perCol].Name
	if line := lineWith(out, col0); !strings.Contains(line, col1) {
		t.Errorf(
			"scrolled column-major: line with %s should also hold %s\n---\n%s",
			col0,
			col1,
			out,
		)
	}
}
