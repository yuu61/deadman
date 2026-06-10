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
	Scale      int
	Precision  string // initial stat-precision label (config "precision"); "" = ms.
	LogDir     string
	ConfigPath string
	Columns    map[string]bool // per-column visibility overrides (config file).
}

// Model is the Bubble Tea model.
type Model struct {
	rows []Row
	opts Options

	width, height int

	// column layout, recomputed on resize.
	hostW, addrW, viaW, resW int

	visible map[string]bool // per-column visibility (config defaults + 'm' toggle).

	scale   int // RTT-bar ms-per-step; adjusted live with up/down, applied when rendering glyphs.
	precIdx int // index into precisionModes for the stat columns; cycled with 'p'.

	scrollTop int // first visible row when the list exceeds the viewport; moved with j/k/g/G/PgUp/PgDn.

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

// New builds the initial model from parsed specs and options.
func New(specs []config.TargetSpec, opts Options) (Model, error) {
	rows, err := buildRows(specs, nil)
	if err != nil {
		return Model{}, err
	}

	return Model{
		rows:     rows,
		opts:     opts,
		scale:    opts.Scale,
		precIdx:  precisionIndex(opts.Precision),
		hostInfo: hostInfo(),
		visible:  buildVisible(opts.Columns),
		warnings: startupWarnings(specs),
	}, nil
}

// buildRows turns specs into rows. Targets present in existing (matched by Key)
// are reused so their history/stats survive a reload.
func buildRows(specs []config.TargetSpec, existing []Row) ([]Row, error) {
	index := map[string]*monitor.Target{}

	for _, r := range existing {
		if r.Target != nil {
			index[r.Target.Key()] = r.Target
		}
	}

	rows := make([]Row, 0, len(specs))
	for _, s := range specs {
		if s.IsSeparator {
			rows = append(rows, Row{Sep: true})

			continue
		}

		t, err := monitor.NewTarget(s.Name, specToPingSpec(s))
		if err != nil {
			return nil, err
		}

		if old, ok := index[t.Key()]; ok {
			rows = append(rows, Row{Target: old})
		} else {
			rows = append(rows, Row{Target: t})
		}
	}

	return rows, nil
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

	rows, err := buildRows(cfg.Targets, existing)
	if err != nil {
		return reload{}, false
	}

	return reload{rows: rows, columns: cfg.Columns, warnings: startupWarnings(cfg.Targets)}, true
}

func hostInfo() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
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

		return m, nil
	case "R":
		return m, func() tea.Msg { return reloadMsg{} }
	}

	return m.handleViewKey(msg), nil
}

// handleViewKey handles the display-only keys: the MIN/MAX ('m') and VIA ('v')
// column toggles, the RTT-bar scale (up/down), and the stat precision ('p'). An
// unknown key leaves the model unchanged.
func (m Model) handleViewKey(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "m":
		// Toggle the MIN/MAX pair: show both unless both are already shown.
		// The header width changes, so the result-bar column is recomputed.
		show := !m.visible[colMin] || !m.visible[colMax]
		m.visible[colMin] = show
		m.visible[colMax] = show

		return m.recalcWidths()
	case "v":
		// Toggle the VIA (probing method) column; its width feeds the result-bar
		// column, so recompute the layout.
		m.visible[colVia] = !m.visible[colVia]

		return m.recalcWidths()
	case "up":
		// Coarser RTT-bar scale (more ms per step). Glyphs are bucketed at render
		// time, so the existing bar re-buckets with no width change.
		m.scale = scaleUp(m.scale)
	case "down":
		// Finer RTT-bar scale.
		m.scale = scaleDown(m.scale)
	case "p":
		// Cycle the stat precision (ms -> ms.1 -> ms.2 -> ms.3); the column width
		// changes, so recompute the result-bar layout.
		m.precIdx = (m.precIdx + 1) % len(precisionModes)

		return m.recalcWidths()
	case "k", "j", "pgup", "pgdown", "g", "G", "home", "end":
		// Scroll the row window. When the list fits the screen these are no-ops
		// (maxTop is 0). The arrows stay bound to the RTT-bar scale.
		return m.scroll(msg.String())
	default:
		// Any other key is unbound; leave the model unchanged.
	}

	return m
}

// scaleSteps is the ladder the up/down keys move the RTT-bar scale through (ms).
var scaleSteps = []int{1, 2, 5, 10, 20, 50, 100}

// scaleUp returns the next coarser (larger-ms) rung above cur. An off-ladder cur
// (e.g. from -s 7) snaps up to the nearest rung, so the live scale stays a free-form
// int while the keys move through sensible presets. At or above the top rung cur is
// preserved: Up means coarser, so it must never decrease the scale (a free-form
// -s 1000 stays 1000 rather than snapping down to the ladder top).
func scaleUp(cur int) int {
	for _, s := range scaleSteps {
		if s > cur {
			return s
		}
	}

	return cur
}

// scaleDown returns the next finer (smaller-ms) rung below cur, clamped at the bottom.
func scaleDown(cur int) int {
	for _, s := range slices.Backward(scaleSteps) {
		if s < cur {
			return s
		}
	}

	return scaleSteps[0]
}

// handleReload reparses the config and starts a fresh generation, so stale
// timers/results from the previous target set are ignored. The live scale/precision
// are intentionally preserved (not reset to config), unlike column visibility:
// scale has a CLI flag (-s) whose value a reload must not silently drop, and
// precision is a live, key-driven view setting.
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

		if m.opts.LogDir != "" {
			_ = monitor.AppendLog(m.opts.LogDir, msg.target, time.Now())
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

	// arrow + host + 1 + addr + 1 [+ via + 1] + statsHeader + 2 + result.
	used := len(arrow) + m.hostW + 1 + m.addrW + 1 + len(m.statsHeader()) + 2
	if m.columnVisible(colVia) {
		used += m.viaW + 1
	}

	m.resW = max(m.width-used, minResultWidth)

	return m
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
// indicator is appended below it.
type viewport struct {
	top, count, maxTop int
	active, status     bool
}

// scrollMetrics derives the visible row window from the terminal height. It is
// the single source of truth shared by View (to slice rows) and the scroll keys
// (to clamp scrollTop), so the rendered window and the clamp can never disagree.
// Before the first WindowSizeMsg (width/height still 0) the whole list is shown.
func (m Model) scrollMetrics() viewport {
	if m.width == 0 || m.height <= 0 {
		return viewport{count: len(m.rows)}
	}

	// usable is 0 when the fixed header fills (or exceeds) the screen. In that case
	// render no rows: forcing a minimum of 1 here would emit height+1 lines, and
	// Bubble Tea drops the top (title) line, defeating the fixed-header guarantee.
	usable := max(m.height-m.fixedHeaderLines(), 0)
	if len(m.rows) <= usable {
		return viewport{count: len(m.rows)}
	}

	// Reserve the bottom usable line for the scroll indicator, unless that would
	// leave no room for a data row (usable <= 1). At usable == 1 (a terminal only
	// one row taller than the header) we deliberately spend that row on data rather
	// than the indicator, so the list still scrolls but shows no position hint.
	status := usable >= 2

	count := usable
	if status {
		count = usable - 1
	}

	maxTop := len(m.rows) - count
	top := max(min(m.scrollTop, maxTop), 0)

	return viewport{top: top, count: count, maxTop: maxTop, active: true, status: status}
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
