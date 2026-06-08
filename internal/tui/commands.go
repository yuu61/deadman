package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/monitor"
	"github.com/yuu61/deadman/internal/ping"
)

// Messages driving the ping loop. Each carries the generation (target-set
// version) it was created in. A reload bumps the model's generation, so stale
// timers and in-flight probe results belonging to a prior target set are ignored
// rather than applied through a now-invalid row index.
type (
	roundStartMsg struct{ gen int }
	pingStartMsg  struct {
		idx int
		gen int
	}
	pingResultMsg struct {
		idx    int
		gen    int
		target *monitor.Target
		res    ping.Result
	}
	reloadMsg struct{}
)

// beginRound emits a roundStartMsg tagged with the current generation.
func (m Model) beginRound() tea.Cmd {
	gen := m.gen

	return func() tea.Msg { return roundStartMsg{gen: gen} }
}

// pingOne returns a command that probes the target at idx and reports the result.
// It carries the *target pointer* so the result is always folded into the correct
// object (like the Python method bound to its target), and is bounds/nil-guarded.
func (m Model) pingOne(idx, gen int) tea.Cmd {
	if idx < 0 || idx >= len(m.rows) || m.rows[idx].Target == nil {
		return nil
	}

	t := m.rows[idx].Target

	return func() tea.Msg {
		return pingResultMsg{
			idx:    idx,
			gen:    gen,
			target: t,
			res:    t.Pinger.Send(context.Background()),
		}
	}
}

// startAsyncRound fires every target concurrently (the original asyncio.gather).
func (m Model) startAsyncRound() (tea.Model, tea.Cmd) {
	m.roundStart = time.Now()
	m.inflight = make([]bool, len(m.rows))

	var cmds []tea.Cmd

	for i, r := range m.rows {
		if r.Target == nil {
			continue
		}

		if c := m.pingOne(i, m.gen); c != nil {
			m.inflight[i] = true // shown as a "pinging now" arrow until the result lands.

			cmds = append(cmds, c)
		}
	}

	m.pending = len(cmds)
	if m.pending == 0 {
		return m, m.scheduleNextRound()
	}

	return m, tea.Batch(cmds...)
}

// scheduleNextRound waits out the remainder of the round interval, then restarts.
func (m Model) scheduleNextRound() tea.Cmd {
	gen := m.gen
	delay := max(allTargetInterval-time.Since(m.roundStart), 0)

	return tea.Tick(delay, func(time.Time) tea.Msg { return roundStartMsg{gen: gen} })
}

// startSyncRound begins probing targets one at a time from the top.
func (m Model) startSyncRound() (tea.Model, tea.Cmd) {
	gen := m.gen

	idx := m.nextTargetIdx(-1)
	if idx < 0 {
		return m, tea.Tick(
			allTargetInterval,
			func(time.Time) tea.Msg { return roundStartMsg{gen: gen} },
		)
	}

	return m, func() tea.Msg { return pingStartMsg{idx: idx, gen: gen} }
}

// advanceSync moves to the next target after a sync probe completes, or pauses at
// the end of the pass before the next round.
func (m Model) advanceSync(cur int) tea.Cmd {
	gen := m.gen

	next := m.nextTargetIdx(cur)
	if next < 0 {
		return tea.Tick(
			allTargetInterval,
			func(time.Time) tea.Msg { return roundStartMsg{gen: gen} },
		)
	}

	return tea.Tick(
		pingInterval,
		func(time.Time) tea.Msg { return pingStartMsg{idx: next, gen: gen} },
	)
}

// nextTargetIdx returns the index of the first non-separator row after `after`,
// or -1 if there is none.
func (m Model) nextTargetIdx(after int) int {
	for i := after + 1; i < len(m.rows); i++ {
		if m.rows[i].Target != nil {
			return i
		}
	}

	return -1
}
