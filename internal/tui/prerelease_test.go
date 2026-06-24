package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
)

// A long-uptime SNT/FAIL count (>=10000) overflows the fixed 5-wide stat header. In the
// single-column layout (no padCell safety net) that previously pushed the row past the
// terminal width, so Bubble Tea clipped the oldest result glyph and bar-starts went
// ragged. After the dynamic stat-width sizing, a layout recompute keeps every row at or
// under the terminal width.
func TestSingleColumnWideCountKeepsRowWidth(t *testing.T) {
	const width = 120

	m := newModel(t, manySpecs(3), Options{Scale: 10})

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

	// Fill history so the bar is full, then force 6-digit SNT/FAIL.
	fillWideCounts(m)

	// A layout recompute (a resize here; in the app, every probe result) picks up the
	// widened stat columns and shrinks the result bar to keep the row aligned.
	_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})
	assertNoLineExceedsWidth(t, out, width)
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

	m := newModel(t, specs, Options{Scale: 10})

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

// Fixing a broken target's config and reloading must replace the always-fail
// placeholder, not reuse it. A placeholder's Key() is just name+addr, which collides
// with a same-named/addressed valid target, so without excluding placeholders from the
// reuse index the fixed target would inherit the placeholder and stay permanently down.
func TestReloadReplacesFixedPlaceholder(t *testing.T) {
	// tcp= without dstport fails to construct -> a placeholder row.
	bad := []config.TargetSpec{
		{Name: "web", Addr: "1.1.1.1", Relay: map[string]string{}, TCP: "noport"},
	}

	rows1, _ := buildRows(bad, nil)
	if !rows1[0].Target.IsFailed() {
		t.Fatalf("expected a failed placeholder for the bad target, got %+v", rows1[0].Target)
	}

	// Reload with the fixed config: same name+addr, now a valid direct-ICMP target.
	good := []config.TargetSpec{{Name: "web", Addr: "1.1.1.1", Relay: map[string]string{}}}

	rows2, _ := buildRows(good, rows1)
	if rows2[0].Target.IsFailed() {
		t.Error(
			"fixed target reused the always-fail placeholder across reload; want a fresh valid target",
		)
	}
}
