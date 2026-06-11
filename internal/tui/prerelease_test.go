package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/monitor"
	"github.com/yuu61/deadman/internal/ping"
)

// A long-uptime SNT/FAIL count (>=10000) overflows the fixed 5-wide stat header. In the
// single-column layout (no padCell safety net) that previously pushed the row past the
// terminal width, so Bubble Tea clipped the oldest result glyph and bar-starts went
// ragged. After the dynamic stat-width sizing, a layout recompute keeps every row at or
// under the terminal width.
func TestSingleColumnWideCountKeepsRowWidth(t *testing.T) {
	const width = 120

	m, err := New(manySpecs(3), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

	// Fill history so the bar is full, then force 6-digit SNT/FAIL.
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

	// A layout recompute (a resize here; in the app, every probe result) picks up the
	// widened stat columns and shrinks the result bar to keep the row aligned.
	_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

	for ln := range strings.SplitSeq(out, "\n") {
		if w := lipgloss.Width(ln); w > width {
			t.Errorf("single-column line exceeds terminal width: %d > %d\n%q", w, width, ln)
		}
	}
}

// A target whose config cannot be constructed (here an snmp relay missing its required
// community) must not abort the whole program: New succeeds, the bad target becomes a
// placeholder row that always fails, and a startup warning names it — so monitoring of
// the valid targets continues.
func TestBadTargetDegradesToPlaceholder(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "good", Addr: "8.8.8.8", Relay: map[string]string{}},
		{Name: "badsnmp", Addr: "1.1.1.1", Relay: map[string]string{"via": "snmp", "relay": "h"}},
	}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatalf("New must not error on a bad target, got %v", err)
	}

	if len(m.rows) != 2 {
		t.Fatalf("got %d rows, want 2 (good + placeholder)", len(m.rows))
	}

	bad := m.rows[1].Target
	if bad == nil {
		t.Fatal("placeholder row has no target")
	}

	if res := bad.Pinger.Send(context.Background()); res.Success {
		t.Errorf("placeholder target should always fail, got %+v", res)
	}

	found := false

	for _, w := range m.warnings {
		if strings.Contains(w, "badsnmp") {
			found = true
		}
	}

	if !found {
		t.Errorf("expected a startup warning naming the bad target; warnings=%v", m.warnings)
	}
}

// logCommand runs the (blocking) log write off the Update goroutine, so it must not read
// the live *Target there — only Update may touch target stats. This snapshots the logged
// fields up front; under -race, concurrently mutating the target while the command runs
// proves the command never reads it.
func TestLogCommandSnapshotsTargetNoRace(t *testing.T) {
	dir := t.TempDir()

	tg, err := monitor.NewTarget("h", ping.Spec{Addr: "1.2.3.4", Relay: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}

	tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 1})

	cmd := logCommand(dir, tg, ping.Result{Success: true, Code: ping.Success, RTT: 2})

	done := make(chan struct{})

	go func() {
		_ = cmd() // writes the log from the snapshot, off the "Update" goroutine.

		close(done)
	}()

	// Concurrently mutate the live target; the snapshot must mean cmd never reads it.
	for range 1000 {
		tg.Consume(ping.Result{Success: true, Code: ping.Success, RTT: 3})
	}

	<-done
}
