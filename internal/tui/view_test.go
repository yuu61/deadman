package tui

import (
	"os"
	"path/filepath"
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

		next, ok := nm.(Model)
		if !ok {
			t.Fatalf("Update returned %T, want Model", nm)
		}

		m = next
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

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)

	for _, want := range []string{
		"Dead Man", "HOSTNAME", "ADDRESS", "LOSS",
		"MIN", "MAX", "JIT", "FAIL", // the added statistics columns.
		"host1", "1.2.3.4", "host2", "5.6.7.8", "▁",
		// The footer lists every key (always expanded, no toggle).
		"(q)uit", "(r)efresh", "(R)eload", "(m)in/max",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("View output missing %q\n---\n%s", want, out)
		}
	}
	// The separator row renders as a run of dashes.
	if !strings.Contains(out, "----------") {
		t.Errorf("View output missing separator dashes\n---\n%s", out)
	}
}

func TestViaColumnAndToggle(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "cf", Addr: "1.1.1.1", Relay: map[string]string{"nexthop": "10.98.38.9"}},
	}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	// VIA column shown by default, labeling the probing method + its differentiator.
	for _, want := range []string{"VIA", "nexthop 10.98.38.9", "(v)ia"} {
		if !strings.Contains(out, want) {
			t.Errorf("default view missing %q\n---\n%s", want, out)
		}
	}

	// 'v' hides the VIA column.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	for _, gone := range []string{"VIA", "nexthop 10.98.38.9"} {
		if strings.Contains(out, gone) {
			t.Errorf("after toggle: %q should be hidden\n---\n%s", gone, out)
		}
	}

	// 'v' again restores it.
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if !strings.Contains(out, "nexthop 10.98.38.9") {
		t.Errorf("after second toggle: VIA should be back\n---\n%s", out)
	}
}

func TestColumnsConfigHidesViaAtStart(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "cf", Addr: "1.1.1.1", Relay: map[string]string{"nexthop": "10.98.38.9"}},
	}

	m, err := New(specs, Options{Scale: 10, Columns: map[string]bool{"VIA": false}})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(out, "VIA") || strings.Contains(out, "nexthop 10.98.38.9") {
		t.Errorf("columns VIA=off should hide the VIA column at startup\n---\n%s", out)
	}
}

func TestParseWarningShown(t *testing.T) {
	// A name with spaces leaves stray tokens; the startup warning surfaces them.
	specs := []config.TargetSpec{{
		Name:    "Cloudflare",
		Addr:    "via",
		Relay:   map[string]string{"nexthop": "10.98.38.9"},
		Dropped: []string{"MGMT", "1.1.1.1"},
	}}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, want := range []string{"ignored stray tokens", "MGMT 1.1.1.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing parse warning %q\n---\n%s", want, out)
		}
	}
}

func TestUnterminatedQuoteWarningShown(t *testing.T) {
	specs := []config.TargetSpec{{
		Name:              "host",
		Addr:              "1.2.3.4",
		Relay:             map[string]string{"user": "admin relay=jump"},
		UnterminatedQuote: true,
	}}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "unterminated quote") {
		t.Errorf("view missing unterminated-quote warning\n---\n%s", out)
	}
}

func TestMinMaxToggleHidesColumns(t *testing.T) {
	specs := []config.TargetSpec{{Name: "host1", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)
	// MIN/MAX shown by default.
	for _, want := range []string{"MIN", "MAX", "JIT", "FAIL"} {
		if !strings.Contains(out, want) {
			t.Errorf("before toggle: output missing %q\n---\n%s", want, out)
		}
	}

	// 'm' hides MIN/MAX; JIT/FAIL and the rest stay.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})

	for _, gone := range []string{"MIN", "MAX"} {
		if strings.Contains(out, gone) {
			t.Errorf("after toggle: %q should be hidden\n---\n%s", gone, out)
		}
	}

	for _, want := range []string{"LOSS", "JIT", "FAIL"} {
		if !strings.Contains(out, want) {
			t.Errorf("after toggle: output missing %q\n---\n%s", want, out)
		}
	}

	// 'm' again restores them.
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !strings.Contains(out, "MIN") || !strings.Contains(out, "MAX") {
		t.Errorf("after second toggle: MIN/MAX should be back\n---\n%s", out)
	}
}

func TestColumnsConfigHidesAtStart(t *testing.T) {
	specs := []config.TargetSpec{{Name: "host1", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m, err := New(specs, Options{Scale: 10, Columns: map[string]bool{"MIN": false, "MAX": false}})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)
	// Config hides MIN/MAX from the very first render (no key needed).
	if strings.Contains(out, "MIN") || strings.Contains(out, "MAX") {
		t.Errorf("MIN/MAX should be hidden by config at startup\n---\n%s", out)
	}

	for _, want := range []string{"LOSS", "RTT", "AVG", "JIT", "SNT", "FAIL"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestViewEmptyBeforeSize(t *testing.T) {
	m, err := New(
		[]config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}},
		Options{Scale: 10},
	)
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

	m, _ = drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 100, Height: 20},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
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
	drive(
		t,
		async,
		tea.WindowSizeMsg{Width: 100, Height: 20},
		pingResultMsg{idx: 5, gen: 99, target: nil, res: ping.Result{}}, // stale gen, oob idx.
		pingResultMsg{
			idx:    5,
			gen:    0,
			target: nil,
			res:    ping.Result{},
		}, // current gen, oob idx, nil target.
		pingStartMsg{
			idx: 5,
			gen: 0,
		}, // oob idx -> pingOne must not deref.
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

	m, err := New(
		specs,
		Options{Scale: 10},
	) // ConfigPath empty: reload bumps gen without changing rows.
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
	oldGen := m.gen

	m, _ = drive(t, m, reloadMsg{})
	if m.gen == oldGen {
		t.Fatal("reload did not bump the generation")
	}

	m, _ = drive(
		t,
		m,
		pingResultMsg{
			idx:    0,
			gen:    oldGen,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)
	if m.rows[0].Target.Snt != 0 {
		t.Errorf("stale-generation result was applied: Snt = %d, want 0", m.rows[0].Target.Snt)
	}

	m, _ = drive(
		t,
		m,
		pingResultMsg{
			idx:    0,
			gen:    m.gen,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)
	if m.rows[0].Target.Snt != 1 {
		t.Errorf("current-generation result not applied: Snt = %d, want 1", m.rows[0].Target.Snt)
	}
}

func TestPrecisionCycle(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)
	// Default: integer ms. (The footer label is the unambiguous discriminator; the
	// rendered numbers nest as substrings — " 5.0" ⊂ " 5.00" ⊂ " 5.000" — so each
	// step's number is only checked against that step's freshly rendered output.)
	if !strings.Contains(out, "(p)recision[ms]") {
		t.Errorf("default footer should show (p)recision[ms]\n---\n%s", out)
	}

	// 'p' -> ms.1: RTT 5 renders with one decimal.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !strings.Contains(out, "(p)recision[ms.1]") || !strings.Contains(out, "5.0") {
		t.Errorf("after p: want (p)recision[ms.1] and 5.0\n---\n%s", out)
	}

	// 'p' -> ms.2: two decimals.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !strings.Contains(out, "(p)recision[ms.2]") || !strings.Contains(out, "5.00") {
		t.Errorf("after p,p: want (p)recision[ms.2] and 5.00\n---\n%s", out)
	}

	// 'p' -> ms.3: three decimals (µs resolution, still in ms units).
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !strings.Contains(out, "(p)recision[ms.3]") || !strings.Contains(out, "5.000") {
		t.Errorf("after p,p,p: want (p)recision[ms.3] and 5.000\n---\n%s", out)
	}

	// 'p' wraps back to ms.
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !strings.Contains(out, "(p)recision[ms]") {
		t.Errorf("after p×4: should wrap to (p)recision[ms]\n---\n%s", out)
	}
}

func TestScaleStepKeys(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "RTT Scale 10ms") {
		t.Errorf("initial footer should show scale 10\n---\n%s", out)
	}

	// down steps to a finer scale (10 -> 5).
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(out, "RTT Scale 5ms") {
		t.Errorf("after down: want scale 5\n---\n%s", out)
	}

	// up steps coarser (5 -> 10 -> 20).
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyUp})
	if !strings.Contains(out, "RTT Scale 20ms") {
		t.Errorf("after up,up: want scale 20\n---\n%s", out)
	}

	// down past the bottom rung clamps at 1ms.
	_, out = drive(t, m,
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(out, "RTT Scale 1ms") {
		t.Errorf("down past the bottom should clamp at 1ms\n---\n%s", out)
	}
}

func TestScaleRebucketsExistingBar(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	// One probe at RTT 15 renders ▂ at scale 10.
	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 15},
		},
	)
	if !strings.Contains(out, "▂") {
		t.Errorf("at scale 10, RTT 15 should render ▂\n---\n%s", out)
	}

	// 'down' to scale 5 re-buckets the SAME stored result to ▄ (no new probe).
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(out, "▄") || strings.Contains(out, "▂") {
		t.Errorf("after down to scale 5, RTT 15 should re-bucket to ▄ not ▂\n---\n%s", out)
	}
}

func TestPrecisionFromConfigAtStartup(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	// A config "precision ms.1" reaches the model via Options.Precision and must take
	// effect at the first paint, before any key press.
	m, err := New(specs, Options{Scale: 10, Precision: "ms.1"})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		pingResultMsg{
			idx:    0,
			target: m.rows[0].Target,
			res:    ping.Result{Success: true, Code: ping.Success, RTT: 5},
		},
	)
	if !strings.Contains(out, "(p)recision[ms.1]") || !strings.Contains(out, "5.0") {
		t.Errorf("config precision ms.1 should render one decimal at startup\n---\n%s", out)
	}
}

func TestReloadPreservesScaleAndPrecision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deadman.conf")
	// The file hides MIN and carries no scale/precision directive.
	err := os.WriteFile(path, []byte("columns MIN=off\nh 1.2.3.4\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m, err := New(specs, Options{Scale: 10, ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}

	// Live: step the scale to 5 and cycle precision to ms.1.
	m, _ = drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}},
	)

	// Reload reparses the file: columns reset to it (MIN hidden), but the live
	// scale/precision are preserved (the documented, intentional asymmetry).
	_, out := drive(t, m, reloadMsg{})

	if !strings.Contains(out, "RTT Scale 5ms") || !strings.Contains(out, "(p)recision[ms.1]") {
		t.Errorf("reload should preserve the live scale/precision\n---\n%s", out)
	}

	if strings.Contains(out, "MIN") {
		t.Errorf("reload should reset columns to the file (MIN hidden)\n---\n%s", out)
	}
}
