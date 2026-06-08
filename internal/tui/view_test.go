package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

// drive renders the model after applying a sequence of messages.
func drive(t *testing.T, m Model, msgs ...tea.Msg) (Model, string) {
	t.Helper()
	for _, msg := range msgs {
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}
	return m, m.View()
}

func TestViewRendersTargetsAndSeparator(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "host1", Addr: "1.2.3.4", Relay: map[string]string{}},
		{IsSeparator: true},
		{Name: "host2", Addr: "5.6.7.8", Relay: map[string]string{}},
	}
	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{idx: 0, target: m.rows[0].Target, res: ping.Result{Success: true, Code: ping.Success, RTT: 5}},
	)

	for _, want := range []string{"Dead Man", "HOSTNAME", "ADDRESS", "LOSS", "host1", "1.2.3.4", "host2", "5.6.7.8", "▁"} {
		if !strings.Contains(out, want) {
			t.Errorf("View output missing %q\n---\n%s", want, out)
		}
	}
	// The separator row renders as a run of dashes.
	if !strings.Contains(out, "----------") {
		t.Errorf("View output missing separator dashes\n---\n%s", out)
	}
}

func TestViewEmptyBeforeSize(t *testing.T) {
	m, err := New([]config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}
	if out := m.View(); out != "" {
		t.Errorf("expected empty view before WindowSizeMsg, got %q", out)
	}
}

func TestRefreshKeyResetsStats(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}
	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}
	m, _ = drive(t, m,
		tea.WindowSizeMsg{Width: 100, Height: 20},
		pingResultMsg{idx: 0, target: m.rows[0].Target, res: ping.Result{Success: true, Code: ping.Success, RTT: 5}},
	)
	if m.rows[0].Target.Snt != 1 {
		t.Fatalf("Snt = %d, want 1", m.rows[0].Target.Snt)
	}
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.rows[0].Target.Snt != 0 {
		t.Errorf("after 'r', Snt = %d, want 0", m.rows[0].Target.Snt)
	}
}

// A reload (or any generation bump) must not let stale, in-flight probe messages
// panic by indexing a shrunken row set, nor fold results into the wrong target.
func TestStaleMessagesDoNotPanic(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	async, _ := New(specs, Options{Scale: 10, Async: true})
	drive(t, async,
		tea.WindowSizeMsg{Width: 100, Height: 20},
		pingResultMsg{idx: 5, gen: 99, target: nil, res: ping.Result{}}, // stale gen, oob idx
		pingResultMsg{idx: 5, gen: 0, target: nil, res: ping.Result{}},  // current gen, oob idx, nil target
		pingStartMsg{idx: 5, gen: 0},                                    // oob idx -> pingOne must not deref
	)

	sync, _ := New(specs, Options{Scale: 10})
	drive(t, sync,
		tea.WindowSizeMsg{Width: 100, Height: 20},
		pingStartMsg{idx: 5, gen: 0},
	)
}

// A generation bump (as a reload performs) must cause results from the previous
// generation to be discarded, while current-generation results still apply.
func TestGenerationGatingIgnoresStaleResults(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}
	m, err := New(specs, Options{Scale: 10}) // ConfigPath empty: reload bumps gen without changing rows
	if err != nil {
		t.Fatal(err)
	}
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
	oldGen := m.gen

	m, _ = drive(t, m, reloadMsg{})
	if m.gen == oldGen {
		t.Fatalf("reload did not bump the generation")
	}

	m, _ = drive(t, m, pingResultMsg{idx: 0, gen: oldGen, target: m.rows[0].Target, res: ping.Result{Success: true, Code: ping.Success, RTT: 5}})
	if m.rows[0].Target.Snt != 0 {
		t.Errorf("stale-generation result was applied: Snt = %d, want 0", m.rows[0].Target.Snt)
	}

	m, _ = drive(t, m, pingResultMsg{idx: 0, gen: m.gen, target: m.rows[0].Target, res: ping.Result{Success: true, Code: ping.Success, RTT: 5}})
	if m.rows[0].Target.Snt != 1 {
		t.Errorf("current-generation result not applied: Snt = %d, want 1", m.rows[0].Target.Snt)
	}
}
