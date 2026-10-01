// Package tui implements the deadman terminal UI with Bubble Tea. It follows the
// Elm architecture: Update mutates model state in a single goroutine (so target
// stats need no locks), View renders the whole screen each frame, and probes run
// in commands (goroutines) that feed results back as messages.
package tui

import (
	"fmt"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/monitoring"
)

// Options holds what the TUI shows besides the monitored rows: facts about this host
// and build, the display flags from the command line, and the terminal's glyph
// capability. The composition root gathers them; the TUI only renders them.
type Options struct {
	Hostname    string
	HostAddress string
	Version     string // build version label shown in the title bar (Makefile -ldflags); "" = "dev".
	Blink       bool   // blink the in-flight arrows in async mode.
	Display     Flags  // display flags from the command line.
	// BlockGlyphsOK reports whether the terminal renders the block elements; it is
	// asked only when the glyph set is auto. nil counts as renderable.
	BlockGlyphsOK func() bool
}

// Model is the Bubble Tea model. Its state is grouped by concern: the link to the
// monitoring session, the view preferences the keys adjust, the layout the terminal size
// and the rows make of them, and the probing indicator. The groups are embedded, so their
// fields and methods read as the model's own.
type Model struct {
	link
	viewPrefs
	layout
	indicator

	opts        Options
	hostInfo    string
	flagWarns   []string // warnings about the command-line flags; kept across reloads.
	configWarns []string // warnings of the latest config read (e.g. rp_filter).
	logDropped  int      // result lines the log has dropped, as the session last reported.
	logError    string   // the first persistent storage failure.
	warnings    []string // header warnings: flagWarns, configWarns, then the log's.
}

// link ties the model to monitoring: the session, and the frontend's detached
// snapshots of its rows.
type link struct {
	session *monitoring.Session // owns the rows; schedules, records, reloads and cancels the probes.
	lines   []monitoring.Line   // the table's lines in config order, with detached snapshots.
	labels  []string            // each line's VIA label; a line's plan changes only with the table.
}

// indicator shows the probing as last reported by the session: the spinner and the
// arrows.
type indicator struct {
	async    bool   // targets are probed in parallel (the session's mode, fixed at start).
	tick     int    // round counter, drives the spinner.
	arrowIdx int    // sync mode: target currently being probed.
	blinkOn  bool   // async + blink: arrow visibility toggle.
	inflight []bool // async mode: which rows are being probed, as last reported.
}

// New builds the model over an open session, with loaded the session's config read:
// its display directives are resolved against the command-line flags, and its warnings
// are shown in the header.
func New(session *monitoring.Session, loaded monitoring.Loaded, opts Options) Model {
	prefs, flagWarns := resolveDisplay(opts.Display, loaded.Display, opts.BlockGlyphsOK)

	m := Model{
		link:        link{session: session},
		viewPrefs:   prefs,
		indicator:   indicator{async: session.Async()},
		opts:        opts,
		hostInfo:    formatHostInfo(opts.Hostname, opts.HostAddress),
		flagWarns:   flagWarns,
		configWarns: diagnosticLines(loaded.Warnings),
	}

	return m.composeWarnings().refreshRows()
}

// formatHostInfo renders facts supplied by the composition root.
func formatHostInfo(host, address string) string {
	if host == "" {
		host = "unknown"
	}

	if address != "" {
		return fmt.Sprintf("From: %s (%s)", host, address)
	}

	return "From: " + host
}

// reloadMsg asks for a config reload (SIGHUP on Unix; the R key calls the same
// Session.Reload directly).
type reloadMsg struct{}

// taskCommand runs task as a command. A task with nothing to report sends no message.
func taskCommand(task monitoring.Task) tea.Cmd {
	return func() tea.Msg {
		event, ok := task()
		if !ok {
			return nil
		}

		return event
	}
}

// Init starts the first ping round immediately.
func (m Model) Init() tea.Cmd {
	return taskCommand(m.session.Start())
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
		return m, taskCommand(m.session.Reload())
	case monitoring.Event:
		return m.handleMonitoring(msg)
	}

	return m, nil
}

// Close stops monitoring and any pending reload, including after a program error or
// kill.
func (m Model) Close() { m.session.Close() }

// composeWarnings builds the header warning list: the flag warnings (e.g. an invalid -s)
// lead, then the config's startup/build warnings, then the log's dropped lines. New, a
// finished reload and a dropped log line all go through it, so a reload that regenerates
// the config warnings cannot silently drop the flag ones — the rejected flag they
// describe is still in force-by-fallback after a reload — nor the log's, which counts
// over the whole run.
func (m Model) composeWarnings() Model {
	w := append(slices.Clone(m.flagWarns), m.configWarns...)
	if m.logDropped > 0 {
		w = append(w, logDroppedLine(m.logDropped))
	}

	if m.logError != "" {
		w = append(w, "log write failed: "+m.logError)
	}

	m.warnings = w

	return m
}

// refreshRows takes the session's detached table, and labels its lines.
func (m Model) refreshRows() Model {
	m.lines = m.session.Table()

	m.labels = make([]string, len(m.lines))
	for i, line := range m.lines {
		m.labels[i] = rowLabel(line)
	}

	return m
}

// handleMonitoring adapts application events to visual feedback. Scheduling,
// cancellation, stale-event rejection, result recording and reload sequencing belong to
// Session; the model keeps display copies of what it reports.
func (m Model) handleMonitoring(event monitoring.Event) (tea.Model, tea.Cmd) {
	out := m.session.Update(event, time.Now())
	m = m.showProbing(out)

	if out.Reload != nil {
		m = m.showReload(*out.Reload)
	}

	if out.Recorded != nil {
		m = m.showRecorded(*out.Recorded)
	}

	if out.LogDropped > 0 {
		m = m.showLogDropped(out.LogDropped)
	}

	if out.LogError != nil && m.logError == "" {
		m.logError = out.LogError.Error()
		m = m.composeWarnings().recalcWidths().clampScroll()
	}

	cmds := make([]tea.Cmd, 0, len(out.Tasks))
	for _, task := range out.Tasks {
		cmds = append(cmds, taskCommand(task))
	}

	return m, tea.Batch(cmds...)
}

// showProbing steps the probing indicator as a transition reports it: the spinner, the
// blinking, the sync arrow and the async in-flight rows.
func (m Model) showProbing(out monitoring.Transition) Model {
	if out.RoundStarted {
		m.tick++
		if m.opts.Blink {
			m.blinkOn = !m.blinkOn
		}
	}

	// The spinner takes a second step when an async pass completes; a sync pass
	// advances it once, at its start.
	if out.RoundCompleted && m.async {
		m.tick++
	}

	if out.Probing != nil {
		m.arrowIdx = *out.Probing
	}

	if out.Inflight != nil {
		m.inflight = out.Inflight
	}

	return m
}

// showRecorded follows a recorded row to its new reading, which also ends its probe of
// the round, widening the stat columns if it needs them wider.
func (m Model) showRecorded(r monitoring.Recorded) Model {
	if row, ok := m.lines[r.Index].(monitoring.Monitored); ok {
		row.Target = row.Target.Advance(r.Reading)
		m.lines[r.Index] = row
	}

	if r.Index < len(m.inflight) {
		m.inflight[r.Index] = false
	}

	return m.growStatWidths(r.Reading.Stats)
}

// showLogDropped shows how many result lines the log has dropped. The warning's first
// appearance adds a header line, so only then is the layout recomputed.
func (m Model) showLogDropped(n int) Model {
	first := m.logDropped == 0
	m.logDropped = n
	m = m.composeWarnings()

	if first {
		m = m.recalcWidths().clampScroll()
	}

	return m
}

// showReload shows a finished reload: its warnings replace the config ones, and when
// its rows were applied the model takes their snapshots and the reread column
// visibility. The other display directives keep their live values (the operator may
// have changed them with keys), so only the configured columns are reapplied. The
// header's height follows the warnings, so the layout is recomputed either way.
func (m Model) showReload(r monitoring.ReloadOutcome) Model {
	m.configWarns = diagnosticLines(r.Warnings)
	m = m.composeWarnings()

	if r.Applied {
		m = m.refreshRows()
		m.visible = buildVisible(r.Display.Columns)
	}

	m = m.recalcWidths().clampScroll()

	return m
}

// rows returns the frontend-owned snapshots, refreshed by monitoring events.
func (m Model) rows() []monitoring.Line { return m.lines }

// refreshActiveRows updates snapshots after resetting statistics, preserving static rows.
func (m Model) refreshActiveRows() Model {
	return m.refreshRows().recalcWidths().clampScroll()
}
