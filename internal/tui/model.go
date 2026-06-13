// Package tui implements the deadman terminal UI with Bubble Tea. It follows the
// Elm architecture: Update mutates model state in a single goroutine (so target
// stats need no locks), View renders the whole screen each frame, and probes run
// in commands (goroutines) that feed results back as messages.
package tui

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/monitor"
	"github.com/yuu61/deadman/internal/ping"
)

// Row is either a ping target or a visual separator.
type Row struct {
	Sep    bool
	Target *monitor.Target
}

// Options holds the command-line configuration plus the column-visibility
// overrides parsed from the config file.
type Options struct {
	Async      bool
	Blink      bool
	Scale      float64 // RTT-bar ms-per-step (the window floor in log mode); 0 lets New fall back.
	Precision  string  // initial stat-precision label (config "precision"); "" = ms.
	LogDir     string
	LogWriter  *monitor.LogWriter // serializes -l log writes off the Update loop; nil = no logging.
	ConfigPath string
	Version    string          // build version label shown in the title bar (Makefile -ldflags); "" = "dev".
	Columns    map[string]bool // per-column visibility overrides (config file).
	Cols       int             // requested newspaper-column count (CLI -c/--split, config "split"); <=1 = single.
	Warnings   []string        // CLI-level startup warnings (e.g. an invalid -s value), shown above the rows.
}

// Model is the Bubble Tea model.
type Model struct {
	rows []Row
	opts Options

	width, height int

	// column layout, recomputed on resize.
	hostW, addrW, viaW, resW int

	// statW is the rendered width of each stat column (keyed by column key), sized to
	// the widest header/cell across targets so a long-uptime SNT/FAIL count (or a
	// >=10000 ms stat) widens its column instead of overflowing the fixed header and
	// shifting the result bar. Recomputed in recalcWidths.
	statW map[string]int

	visible map[string]bool // per-column visibility (config defaults + 'm' toggle).

	scale   float64 // RTT-bar ms-per-step (the window floor in log mode); adjusted live with up/down.
	logIdx  int     // index into logFactors (0 = linear); cycled with 'l'. The selected LnBase drives Glyph.
	precIdx int     // index into precisionModes for the stat columns; cycled with 'p'.

	scrollTop int // first visible row when the list exceeds the viewport; moved with j/k/g/G/PgUp/PgDn.

	cols int // requested newspaper-column count ('['/']'); effectiveCols clamps it to the terminal width.

	tick     int  // round counter, drives the spinner.
	arrowIdx int  // sync mode: target currently being probed.
	blinkOn  bool // async + blink: arrow visibility toggle.

	gen        int       // target-set generation; bumped on reload to drop stale msgs.
	pending    int       // async: probes still in flight this round
	inflight   []bool    // async: per-row, true while that target's probe is running
	roundStart time.Time // async: when the current round began

	hostInfo string
	warnings []string // startup warnings (e.g. rp_filter, IPv6 nexthop ignored).
}

// New builds the initial model from parsed specs and options. The error return is
// retained for call-site stability; New currently never fails (a target whose config
// cannot be built degrades to a permanent-failure row rather than aborting).
func New(specs []config.TargetSpec, opts Options) (Model, error) {
	rows, buildWarns := buildRows(specs, nil)

	// Normalize the scale like Cols: a caller bypassing resolveScale (a test or an
	// embedding) may pass a zero/degenerate Options.Scale, which would otherwise make
	// rttGlyph's scale<=0 guard flatten every bar to ▁. config.ScaleOrDefault is the same
	// "invalid → default" helper the CLI resolver's fallback uses, so behavior is uniform.
	scale := config.ScaleOrDefault(opts.Scale)

	// CLI-level warnings (e.g. an invalid -s) lead, then the per-target startup and build
	// warnings. Clone so appending never mutates the caller's slice.
	warnings := append(slices.Clone(opts.Warnings), startupWarnings(specs)...)
	warnings = append(warnings, buildWarns...)

	return Model{
		rows:     rows,
		opts:     opts,
		scale:    scale,
		precIdx:  precisionIndex(opts.Precision),
		hostInfo: hostInfo(),
		visible:  buildVisible(opts.Columns),
		warnings: warnings,
		cols:     max(opts.Cols, 1),
	}, nil
}

// buildRows turns specs into rows, returning any per-target construction warnings.
// Targets present in existing (matched by Key) are reused so their history/stats
// survive a reload. A target whose config cannot be built (e.g. a missing required
// relay attribute or an option-like operand) is NOT fatal: it becomes a placeholder row
// that always shows X, and the reason is collected as a startup warning, so one bad
// target no longer aborts monitoring of all the others.
func buildRows(specs []config.TargetSpec, existing []Row) ([]Row, []string) {
	index := map[string]*monitor.Target{}

	for _, r := range existing {
		// Skip failed placeholders: a fixed config that reloads to a real target with the
		// same Key() must build fresh, not inherit the always-fail placeholder.
		if r.Target != nil && !r.Target.IsFailed() {
			index[r.Target.Key()] = r.Target
		}
	}

	var warns []string

	rows := make([]Row, 0, len(specs))
	for _, s := range specs {
		if s.IsSeparator {
			rows = append(rows, Row{Sep: true})

			continue
		}

		t, err := monitor.NewTarget(s.Name, specToPingSpec(s))
		if err != nil {
			warns = append(warns, fmt.Sprintf(
				"%q (address=%q): %v — this target shows a permanent failure; "+
					"fix the config and reload (R)",
				s.Name, s.Addr, err,
			))
			rows = append(rows, Row{Target: monitor.NewFailedTarget(s.Name, s.Addr)})

			continue
		}

		if old, ok := index[t.Key()]; ok {
			rows = append(rows, Row{Target: old})
		} else {
			rows = append(rows, Row{Target: t})
		}
	}

	return rows, warns
}

// specToPingSpec maps a parsed config target to the ping.Spec the probe layer
// consumes. buildRows uses it to construct targets; the startup-warning code uses it
// to ask ping.UsesDirectICMP / ping.UsesNexthop which method a target resolves to, so
// both share one mapping and cannot drift.
func specToPingSpec(s config.TargetSpec) ping.Spec {
	return ping.Spec{
		Addr:   s.Addr,
		OSName: s.Relay["os"],
		Source: s.Source,
		TCP:    s.TCP,
		Relay:  s.Relay,
	}
}

// reload is the result of reparsing the config file: the new rows, the
// column-visibility overrides, and any startup warnings.
type reload struct {
	rows     []Row
	columns  map[string]bool
	warnings []string
}

// loadRows reparses the config file for a SIGHUP/manual reload. ok is false when
// the file cannot be read or parsed (the current state is kept).
func loadRows(path string, existing []Row) (reload, bool) {
	// #nosec G304 -- path is the operator-supplied config file, not remote input.
	f, err := os.Open(path)
	if err != nil {
		return reload{}, false
	}
	defer func() { _ = f.Close() }()

	cfg, err := config.ParseConfig(f)
	if err != nil {
		return reload{}, false
	}

	rows, buildWarns := buildRows(cfg.Targets, existing)

	return reload{
		rows:     rows,
		columns:  cfg.Columns,
		warnings: append(startupWarnings(cfg.Targets), buildWarns...),
	}, true
}

// hostLookupTimeout bounds the startup hostname lookup. It runs synchronously before
// the alt-screen is entered, so without a deadline a slow/unreachable resolver would
// hang startup on a blank screen; the looked-up address is only cosmetic (the header's
// "From:" line), which falls back gracefully to the bare hostname.
const hostLookupTimeout = 1 * time.Second

func hostInfo() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	ctx, cancel := context.WithTimeout(context.Background(), hostLookupTimeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err == nil && len(addrs) > 0 {
		return fmt.Sprintf("From: %s (%s)", host, addrs[0])
	}

	return "From: " + host
}

// Init starts the first ping round immediately.
func (m Model) Init() tea.Cmd {
	return m.beginRound()
}

// Update is the Elm-style state transition. It dispatches each message kind to a
// dedicated handler; the handlers do the real work so this stays a thin router.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m = m.recalcWidths()
		m = m.clampScroll()

		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case reloadMsg:
		return m.handleReload()
	case roundStartMsg:
		return m.handleRoundStart(msg)
	case pingStartMsg:
		return m.handlePingStart(msg)
	case pingResultMsg:
		return m.handlePingResult(msg)
	}

	return m, nil
}

// handleKey processes a keypress: the control keys (quit, refresh 'r', reload 'R')
// are handled here; the display-only keys are delegated to handleViewKey.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		for _, r := range m.rows {
			if r.Target != nil {
				r.Target.Refresh()
			}
		}

		// Refresh resets the counters to 0, so the stat columns may shrink back; the
		// per-probe growStatWidths only widens, so recompute the layout here.
		return m.recalcWidths(), nil
	case "R":
		return m, func() tea.Msg { return reloadMsg{} }
	}

	return m.handleViewKey(msg), nil
}

// handleViewKey handles the display-only keys: the MIN/MAX ('m') and structural
// HOSTNAME/ADDRESS/VIA ('h'/'a'/'v') column toggles, the RTT-bar scale (up/down) and
// its log factor ('l'), the stat precision ('p'), and the newspaper-column count
// ('['/']'). An unknown key leaves the model unchanged.
//
// The layout-changing keys clampScroll after recalcWidths: a stat-column toggle
// shifts minColumnWidth, which can change effectiveCols and thus the vertical
// capacity, so the scroll position must be re-pinned just like a column-count change.
func (m Model) handleViewKey(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "m":
		// Toggle the MIN/MAX pair: show both unless both are already shown. The
		// header width changes, so the result-bar column (and the multi-column fit)
		// are recomputed.
		show := !m.visible[colMin] || !m.visible[colMax]
		m.visible[colMin] = show
		m.visible[colMax] = show

		return m.recalcWidths().clampScroll()
	case "h", "a", "v":
		// Toggle a structural string column: HOSTNAME ('h'), ADDRESS ('a') or VIA
		// ('v'). Each one's width feeds the result-bar column and the multi-column
		// fit, so recompute the layout and re-pin the scroll, like the MIN/MAX toggle.
		return m.toggleStructuralCol(msg.String()).recalcWidths().clampScroll()
	case "up", "down", "l":
		// RTT-bar scale (up/down step the ladder) and its log factor ('l' cycles
		// 0->e->e²->0). Glyphs are bucketed at render time, so the bar re-buckets
		// with no width change.
		return m.adjustRTTScale(msg.String())
	case "p":
		// Cycle the stat precision (ms -> ms.1 -> ms.2 -> ms.3); the column width
		// changes, so recompute the result-bar layout.
		m.precIdx = (m.precIdx + 1) % len(precisionModes)

		return m.recalcWidths().clampScroll()
	case "[", "]":
		// Adjust the newspaper-column count (']' more, '[' fewer; effectiveCols clamps
		// it to what the width fits). The count changes both the per-column width and
		// the vertical capacity, so recompute the widths and re-pin the scroll.
		return m.adjustCols(msg.String()).recalcWidths().clampScroll()
	case "k", "j", "pgup", "pgdown", "g", "G", "home", "end":
		// Scroll the row window. When the list fits the screen these are no-ops
		// (maxTop is 0). The arrows stay bound to the RTT-bar scale.
		return m.scroll(msg.String())
	default:
		// Any other key is unbound; leave the model unchanged.
	}

	return m
}

// adjustRTTScale handles the RTT-bar scale and log-factor keys: up/down step the
// scale ladder and 'l' cycles the log factor (0 linear -> e -> e²). None of these
// changes a column width, so the caller needs no width recalc.
func (m Model) adjustRTTScale(key string) Model {
	switch key {
	case "up":
		m.scale = scaleUp(m.scale)
	case "down":
		m.scale = scaleDown(m.scale)
	case "l":
		m.logIdx = (m.logIdx + 1) % len(logFactors)
	default:
		// Unreachable: handleViewKey routes only up/down/l here.
	}

	return m
}

// toggleStructuralCol flips the visibility of the structural string column bound to
// the key: 'h' -> HOSTNAME, 'a' -> ADDRESS, 'v' -> VIA. RESULT has no key and is
// never hidden. The caller recomputes the layout afterwards, since each column's
// width feeds the result-bar column and the multi-column fit.
func (m Model) toggleStructuralCol(key string) Model {
	switch key {
	case "h":
		m.visible[colHost] = !m.visible[colHost]
	case "a":
		m.visible[colAddr] = !m.visible[colAddr]
	case "v":
		m.visible[colVia] = !m.visible[colVia]
	default:
		// Unreachable: handleViewKey routes only "h"/"a"/"v" here.
	}

	return m
}

// adjustCols bumps the requested newspaper-column count: ']' adds one, anything else
// ('[') removes one with a floor of a single column. effectiveCols later clamps the
// request to what the terminal width can actually hold.
func (m Model) adjustCols(key string) Model {
	if key == "]" {
		// Cap one beyond what currently fits so repeated ']' on a narrow terminal
		// can't run the request away (needing as many '[' presses to undo). Hiding
		// stat columns lowers minColumnWidth and lifts the cap, so columns can still
		// grow incrementally.
		m.cols = min(m.cols+1, m.effectiveCols()+1)
	} else {
		m.cols = max(m.cols-1, 1)
	}

	return m
}

// logFactor is one entry in the 'l'-key cycle: a legend label paired with LnBase, the
// log base carried as a value rather than implied by the slice index. Mirrors
// precisionMode (label + behavior in one row).
type logFactor struct {
	Label string
	// LnBase is the ln of the per-step bucket base, i.e. the divisor in
	// ln(rtt/scale)/LnBase handed to monitor.Glyph: 0 = linear (no log), 1 = base e,
	// 2 = base e². Because it is data, not the index, a new factor is just a row — base
	// 10 would be {"x10", math.Log(10)} — with no rttGlyph change. Labels stay ASCII
	// (narrow): "×"/"²" are East-Asian-ambiguous and render 2 cells on CJK terminals,
	// desyncing the keys-line width from the renderer's measure.
	LnBase float64
}

// logFactors is the single source of the 'l'-key cycle order, the log base each entry
// selects, and the floor-mode legend labels for the log factors: linear (no log), then
// base e and base e². Index 0's "lin" label is a cycle-slot placeholder, not rendered:
// keysLine labels linear mode with its own "RTT Scale" wording (vs "RTT floor … xe"),
// since linear shows ms-per-step while a log factor shows the window floor. So the table
// is the single source of the log-factor labels (index > 0); the linear row is self-named.
var logFactors = []logFactor{
	{"lin", 0},
	{"xe", 1},
	{"xe2", 2},
}

// scaleSteps is the ladder the up/down keys move the RTT-bar scale through (ms); it
// extends below 1ms so sub-millisecond LAN RTTs can be resolved.
var scaleSteps = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 50, 100}

// scaleUp returns the next coarser (larger-ms) rung above cur. An off-ladder cur
// (e.g. from -s 7) snaps up to the nearest rung, so the live scale stays a free-form
// value while the keys move through sensible presets. At or above the top rung cur is
// preserved: Up means coarser, so it must never decrease the scale (a free-form
// -s 1000 stays 1000 rather than snapping down to the ladder top).
func scaleUp(cur float64) float64 {
	for _, s := range scaleSteps {
		if s > cur {
			return s
		}
	}

	return cur
}

// scaleDown returns the next finer (smaller-ms) rung below cur. At or below the bottom
// rung cur is preserved: Down means finer, so it must never increase the scale — the
// mirror of scaleUp's top guard, so a free-form -s 0.005 stays 0.005 rather than being
// snapped up to the ladder bottom.
func scaleDown(cur float64) float64 {
	for _, s := range slices.Backward(scaleSteps) {
		if s < cur {
			return s
		}
	}

	return cur
}

// handleReload reparses the config and starts a fresh generation, so stale
// timers/results from the previous target set are ignored. The live
// scale/log-factor/precision are intentionally preserved (not reset to config),
// unlike column visibility: scale has a CLI flag (-s) whose value a reload must not
// silently drop, and the log factor and precision are live, key-driven view settings.
func (m Model) handleReload() (tea.Model, tea.Cmd) {
	if r, ok := loadRows(m.opts.ConfigPath, m.rows); ok {
		m.rows = r.rows
		m.visible = buildVisible(r.columns)
		m.warnings = r.warnings
		m = m.recalcWidths()
		m = m.clampScroll() // a shrunk target set may leave scrollTop past the end.
	}

	m.gen++
	m.pending = 0

	return m, m.beginRound()
}

// handleRoundStart advances the spinner/blink and kicks off the next round.
func (m Model) handleRoundStart(msg roundStartMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}

	m.tick++
	if m.opts.Blink {
		m.blinkOn = !m.blinkOn
	}

	if m.opts.Async {
		return m.startAsyncRound()
	}

	return m.startSyncRound()
}

// handlePingStart marks the target at msg.idx as the one being probed (sync mode).
func (m Model) handlePingStart(msg pingStartMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}

	m.arrowIdx = msg.idx

	return m, m.pingOne(msg.idx, msg.gen)
}

// handlePingResult folds a probe result into its target and schedules what comes
// next (the next async round when all in-flight probes are done, or the next sync
// target otherwise).
func (m Model) handlePingResult(msg pingResultMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}

	if msg.target != nil {
		msg.target.Consume(msg.res)
		// A counter crossing a digit boundary (e.g. SNT reaching 10000) widens its stat
		// column; grow it incrementally (O(1)) rather than re-scanning every target each
		// probe, which would be O(N^2) per round on a large config.
		m = m.growStatWidths(msg.target)

		if m.opts.LogWriter != nil {
			// Snapshot the logged fields on this (Update) goroutine; the LogWriter writes
			// them off it without blocking and with a bounded queue.
			m.opts.LogWriter.Log(
				msg.target.Name,
				msg.res,
				msg.target.Avg,
				msg.target.Snt,
				time.Now(),
			)
		}
	}

	if !m.opts.Async {
		return m, m.advanceSync(msg.idx)
	}

	if msg.idx >= 0 && msg.idx < len(m.inflight) {
		m.inflight[msg.idx] = false // clear this target's "pinging" arrow.
	}

	m.pending--
	if m.pending <= 0 {
		m.tick++ // second spinner step per round.

		return m, m.scheduleNextRound()
	}

	return m, nil
}

// recalcWidths recomputes the dynamic column widths and returns the updated model,
// keeping every Model method a value receiver.
func (m Model) recalcWidths() Model {
	// Seed the floors with a trailing space (len("HOSTNAME ") = 9,
	// len("ADDRESS ") = 8).
	hlen := len("HOSTNAME ")

	for _, r := range m.rows {
		if r.Target == nil {
			continue
		}

		if l := displayWidth(r.Target.Name); l > hlen {
			hlen = l
		}
	}

	if hlen > maxHostnameLength {
		hlen = maxHostnameLength
	}

	m.hostW = hlen

	alen := len("ADDRESS ")

	for _, r := range m.rows {
		if r.Target == nil {
			continue
		}

		if l := displayWidth(r.Target.Addr); l > alen {
			alen = l
		}
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
	if m.cols <= 1 || m.width <= 0 || len(m.rows) == 0 {
		return 1
	}

	fit := (m.width + colGutterWidth) / (m.minColumnWidth() + colGutterWidth)
	eff := max(1, min(m.cols, fit))

	// When the whole list fits the height, only ceil(rows/perColumn) columns hold
	// every row; a larger fit would leave a phantom header-only column. len(m.rows) is
	// >= 1 here (the empty case returned above).
	if usable := m.usableRows(); usable > 0 && len(m.rows) <= usable*eff {
		perCol := ceilDiv(len(m.rows), eff)
		eff = ceilDiv(len(m.rows), perCol)
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

	for _, r := range m.rows {
		if r.Target == nil {
			continue
		}

		if l := displayWidth(r.Target.Via); l > w {
			w = l
		}
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
		return viewport{count: len(m.rows), cols: 1, perCol: len(m.rows)}
	}

	eff := m.effectiveCols()

	// usable is 0 when the fixed header fills (or exceeds) the screen. In that case
	// render no rows: forcing a minimum of 1 here would emit height+1 lines, and
	// Bubble Tea drops the top (title) line, defeating the fixed-header guarantee.
	usable := m.usableRows()

	// The grid holds usable rows per column across eff columns. When the whole list
	// fits, show it all unscrolled; perCol is the column-major column height (column
	// 0 fills first) and is <= usable, so the merged block never overflows the screen.
	if len(m.rows) <= usable*eff {
		return viewport{count: len(m.rows), cols: eff, perCol: ceilDiv(len(m.rows), eff)}
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
	maxTop := len(m.rows) - count
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
	const nonWarnLines = 5 // title, host info, keys, blank separator, header.

	return nonWarnLines + len(m.warnings)
}

// scroll moves the row window in response to a navigation key and clamps the new
// top into range. With a list that fits the screen maxTop is 0, so every key is
// a no-op.
func (m Model) scroll(key string) Model {
	vp := m.scrollMetrics()

	switch key {
	case "k":
		m.scrollTop--
	case "j":
		m.scrollTop++
	case "pgup":
		m.scrollTop -= vp.count
	case "pgdown":
		m.scrollTop += vp.count
	case "g", "home":
		m.scrollTop = 0
	case "G", "end":
		m.scrollTop = vp.maxTop
	default:
		// Unreachable: handleViewKey routes only the keys handled above.
	}

	m.scrollTop = max(min(m.scrollTop, vp.maxTop), 0)

	return m
}

// clampScroll re-pins scrollTop into the current valid range, used after a resize
// or a reload that shrinks the target set. It preserves the position while the list
// still overflows; growing the terminal until everything fits naturally lands at the
// top (scrollMetrics returns top 0 when no scrolling is needed).
func (m Model) clampScroll() Model {
	m.scrollTop = m.scrollMetrics().top

	return m
}
