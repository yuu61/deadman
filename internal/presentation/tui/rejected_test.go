package tui

import (
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestRejectedRowsProjectResultsAndSurviveReset(t *testing.T) {
	src := sourceOf([]config.Line{
		config.Malformed{Name: "bad", Addr: "192.0.2.1", Problem: "invalid config"},
		config.Separator{},
		config.Target{Name: "good", Addr: "192.0.2.2"},
	}, testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{Async: true})

	m, _ = drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		until(func(m Model) bool { return slices.Contains(m.inflight, true) }),
	)
	if len(m.inflight) != 3 || m.inflight[0] || m.inflight[1] || !m.inflight[2] {
		t.Fatalf("inflight=%v", m.inflight)
	}

	m, _ = drive(t, m, answer{row: 2, res: probe.SuccessResult(5)})
	if snapshotAt(t, m, 2).Snt != 1 || !isRejected(m.rows()[0]) {
		t.Fatal("result projected to a rejected row")
	}

	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !isRejected(m.rows()[0]) || snapshotAt(t, m, 2).Snt != 0 {
		t.Fatal("reset lost static row or retained statistics")
	}

	src.cfg.Lines[0] = config.Target{Name: "bad", Addr: "192.0.2.1"} // the line is fixed.

	m, _ = drive(t, m, reloadMsg{})
	if isRejected(m.rows()[0]) || snapshotAt(t, m, 0).Name != "bad" ||
		snapshotAt(t, m, 2).Name != "good" {
		t.Fatal("fixed row not projected after reload")
	}

	m, _ = drive(t, m, answer{row: 2, res: probe.SuccessResult(7)})
	if snapshotAt(t, m, 2).RTT != 7 || snapshotAt(t, m, 0).Snt != 0 {
		t.Fatal("reload used stale target positions")
	}
}
