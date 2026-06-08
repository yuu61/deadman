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
	hostW, addrW, resW int

	visible map[string]bool // per-column visibility (config defaults + 'm' toggle).

	tick     int  // round counter, drives the spinner.
	arrowIdx int  // sync mode: target currently being probed.
	blinkOn  bool // async + blink: arrow visibility toggle.

	gen        int       // target-set generation; bumped on reload to drop stale msgs.
	pending    int       // async: probes still in flight this round
	inflight   []bool    // async: per-row, true while that target's probe is running
	roundStart time.Time // async: when the current round began

	hostInfo string
}

// New builds the initial model from parsed specs and options.
func New(specs []config.TargetSpec, opts Options) (Model, error) {
	rows, err := buildRows(specs, opts.Scale, nil)
	if err != nil {
		return Model{}, err
	}

	return Model{
		rows:     rows,
		opts:     opts,
		hostInfo: hostInfo(),
		visible:  buildVisible(opts.Columns),
	}, nil
}

// buildRows turns specs into rows. Targets present in existing (matched by Key)
// are reused so their history/stats survive a reload.
func buildRows(specs []config.TargetSpec, scale int, existing []Row) ([]Row, error) {
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

		spec := ping.Spec{
			Addr:   s.Addr,
			OSName: s.Relay["os"],
			Source: s.Source,
			TCP:    s.TCP,
			Relay:  s.Relay,
		}

		t, err := monitor.NewTarget(s.Name, spec, scale)
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

// loadRows reparses the config file for a SIGHUP/manual reload, returning the new
// rows and the column-visibility overrides.
func loadRows(path string, scale int, existing []Row) ([]Row, map[string]bool, bool) {
	// #nosec G304 -- path is the operator-supplied config file, not remote input.
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer func() { _ = f.Close() }()

	cfg, err := config.ParseConfig(f)
	if err != nil {
		return nil, nil, false
	}

	rows, err := buildRows(cfg.Targets, scale, existing)
	if err != nil {
		return nil, nil, false
	}

	return rows, cfg.Columns, true
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

// handleKey processes a keypress: quit, refresh stats ('r') or reload ('R').
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
	case "m":
		// Toggle the MIN/MAX pair: show both unless both are already shown.
		// The header width changes, so the result-bar column is recomputed.
		show := !m.visible[colMin] || !m.visible[colMax]
		m.visible[colMin] = show
		m.visible[colMax] = show
		m = m.recalcWidths()

		return m, nil
	}

	return m, nil
}

// handleReload reparses the config and starts a fresh generation, so stale
// timers/results from the previous target set are ignored.
func (m Model) handleReload() (tea.Model, tea.Cmd) {
	if rows, cols, ok := loadRows(m.opts.ConfigPath, m.opts.Scale, m.rows); ok {
		m.rows = rows
		m.visible = buildVisible(cols)
		m = m.recalcWidths()
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

	// arrow + host + 1 + addr + 1 + statsHeader + 2 + result.
	used := len(arrow) + m.hostW + 1 + m.addrW + 1 + len(m.statsHeader()) + 2
	m.resW = max(m.width-used, minResultWidth)

	return m
}
