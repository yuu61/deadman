package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// The 'h' and 'a' keys hide and restore the structural HOSTNAME and ADDRESS columns,
// and the "columns" directive can hide them at startup — bringing them to VIA's level
// (every column but RESULT is toggleable).
func TestHostAddrColumnToggle(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "alpha", Addr: "203.0.113.7", Relay: map[string]string{}},
	}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, want := range []string{"HOSTNAME", "ADDRESS", "alpha", "203.0.113.7"} {
		if !strings.Contains(out, want) {
			t.Errorf("default view missing %q\n---\n%s", want, out)
		}
	}

	// 'h' hides HOSTNAME (header label and the target name), ADDRESS stays.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if strings.Contains(out, "HOSTNAME") || strings.Contains(out, "alpha") {
		t.Errorf("after 'h' the HOSTNAME column should be hidden\n---\n%s", out)
	}

	if !strings.Contains(out, "203.0.113.7") {
		t.Errorf("'h' must not hide the ADDRESS column\n---\n%s", out)
	}

	// 'a' now also hides ADDRESS; both identity columns are gone (only RESULT-side
	// content remains), which the user explicitly allows.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if strings.Contains(out, "ADDRESS") || strings.Contains(out, "203.0.113.7") {
		t.Errorf("after 'a' the ADDRESS column should be hidden too\n---\n%s", out)
	}

	// 'h' then 'a' restore both.
	_, out = drive(
		t, m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}},
	)
	for _, want := range []string{"alpha", "203.0.113.7"} {
		if !strings.Contains(out, want) {
			t.Errorf("after restoring, %q should be back\n---\n%s", want, out)
		}
	}
}

// columns ADDRESS=off / HOSTNAME=off hide the structural columns at startup.
func TestColumnsConfigHidesHostAddrAtStart(t *testing.T) {
	specs := []config.TargetSpec{
		{Name: "beta", Addr: "198.51.100.9", Relay: map[string]string{}},
	}

	m, err := New(specs, Options{Scale: 10, Columns: map[string]bool{"ADDRESS": false}})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(out, "ADDRESS") || strings.Contains(out, "198.51.100.9") {
		t.Errorf("columns ADDRESS=off should hide the ADDRESS column at startup\n---\n%s", out)
	}

	// HOSTNAME is unaffected and still labels the row.
	if !strings.Contains(out, "beta") {
		t.Errorf("ADDRESS=off must not hide HOSTNAME\n---\n%s", out)
	}
}

// The structural-column gating in headerLine must use the exact same visibility
// conditions as rowFixedWidth, or the result bar is mis-sized. For every combination
// of HOSTNAME/ADDRESS/VIA visibility, the header's display width must equal the fixed
// width plus the always-on RESULT label.
func TestStructuralGatingMatchesRowFixedWidth(t *testing.T) {
	specs := []config.TargetSpec{
		{
			Name:  "host-longname",
			Addr:  "203.0.113.45",
			Relay: map[string]string{"nexthop": "10.0.0.1"},
		},
	}

	m, err := New(specs, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 40})

	for _, host := range []bool{true, false} {
		for _, addr := range []bool{true, false} {
			for _, via := range []bool{true, false} {
				m.visible[colHost] = host
				m.visible[colAddr] = addr
				m.visible[colVia] = via

				// lipgloss.Width ignores the bold SGR escapes headerLine adds.
				got := lipgloss.Width(m.headerLine())
				if want := m.rowFixedWidth() + len("RESULT"); got != want {
					t.Errorf(
						"host=%v addr=%v via=%v: headerLine width %d != rowFixedWidth+RESULT %d",
						host, addr, via, got, want,
					)
				}
			}
		}
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

// scaleUp/scaleDown move through the preset ladder, but the live scale may be a
// free-form CLI/config value off the ladder. The key invariant: Up (coarser) must
// never decrease the scale and Down (finer) must never increase it, even above the
// top rung or below the bottom one.
func TestScaleLadderBounds(t *testing.T) {
	cases := []struct {
		name           string
		cur            int
		wantUp, wantDn int
	}{
		{"within ladder", 10, 20, 5},
		{"off-ladder below top", 7, 10, 5},
		{"at top rung", 100, 100, 50},
		{"above top rung stays put on up", 1000, 1000, 100}, // the Bug-1 regression guard.
		{"at bottom rung", 1, 2, 1},
		{"off-ladder near bottom", 3, 5, 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scaleUp(c.cur); got != c.wantUp {
				t.Errorf("scaleUp(%d) = %d, want %d", c.cur, got, c.wantUp)
			}

			if got := scaleDown(c.cur); got != c.wantDn {
				t.Errorf("scaleDown(%d) = %d, want %d", c.cur, got, c.wantDn)
			}
		})
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

// manySpecs builds n direct-ICMP targets named h000, h001, … so each name is a
// unique, non-overlapping substring for View assertions.
func manySpecs(n int) []config.TargetSpec {
	specs := make([]config.TargetSpec, n)
	for i := range specs {
		specs[i] = config.TargetSpec{
			Name:  fmt.Sprintf("h%03d", i),
			Addr:  fmt.Sprintf("10.0.0.%d", i+1),
			Relay: map[string]string{},
		}
	}

	return specs
}

// lineWith returns the first line of out containing sub, or "".
func lineWith(out, sub string) string {
	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}

	return ""
}

// A list that fits the terminal renders every row and shows no scroll indicator,
// so small configs look exactly as before the viewport existed.
func TestViewportSmallFitsNoScroll(t *testing.T) {
	m, err := New(manySpecs(3), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, want := range []string{"h000", "h001", "h002"} {
		if !strings.Contains(out, want) {
			t.Errorf("small list should show every row, missing %q\n---\n%s", want, out)
		}
	}

	if strings.Contains(out, "j/k scroll") {
		t.Errorf("a list that fits must not show the scroll indicator\n---\n%s", out)
	}
}

// A list taller than the terminal renders only the visible window plus a one-line
// position indicator; rows below the fold are absent.
func TestViewportWindowAndStatus(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})

	if !strings.Contains(out, "h000") || !strings.Contains(out, "h005") {
		t.Errorf("top of the window should be visible\n---\n%s", out)
	}

	if strings.Contains(out, "h045") || strings.Contains(out, "h049") {
		t.Errorf("rows below the fold must not render\n---\n%s", out)
	}

	for _, want := range []string{"j/k scroll", "/50]"} {
		if !strings.Contains(out, want) {
			t.Errorf("scroll indicator missing %q\n---\n%s", want, out)
		}
	}

	// The fixed header stays put (it is never part of the scrolled window).
	if !strings.Contains(out, "HOSTNAME") || !strings.Contains(out, "Dead Man") {
		t.Errorf("fixed header must remain when scrolled\n---\n%s", out)
	}
}

// g/G jump to the ends, j past the bottom clamps, and PgUp walks back up.
func TestScrollKeysMoveAndClamp(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})

	// G -> bottom: last row visible, first gone, indicator ends at the total.
	mBot, out := drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if !strings.Contains(out, "h049") || strings.Contains(out, "h000") {
		t.Errorf("G should reveal the bottom\n---\n%s", out)
	}

	if !strings.Contains(out, "-50/50]") {
		t.Errorf("indicator should reach the end after G\n---\n%s", out)
	}

	// j at the bottom is a no-op (clamped), not an over-scroll.
	_, out = drive(t, mBot, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if !strings.Contains(out, "-50/50]") || !strings.Contains(out, "h049") {
		t.Errorf("j past the bottom must clamp\n---\n%s", out)
	}

	// g -> top.
	_, out = drive(t, mBot, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if !strings.Contains(out, "h000") || strings.Contains(out, "h049") {
		t.Errorf("g should return to the top\n---\n%s", out)
	}

	// PgUp from the bottom moves up by a page (away from the last row).
	_, out = drive(t, mBot, tea.KeyMsg{Type: tea.KeyPgUp})
	if strings.Contains(out, "h049") {
		t.Errorf("PgUp from the bottom should scroll up\n---\n%s", out)
	}
}

// Growing the terminal so the list fits drops the scroll state back to a full,
// unscrolled view; shrinking re-engages the viewport from the top.
func TestViewportResizeReclamps(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	// Scroll to the bottom while overflowing.
	m, _ = drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 20},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}},
	)
	if m.scrollTop == 0 {
		t.Fatal("precondition: expected a non-zero scrollTop after G, got 0")
	}

	// Grow tall enough to fit all 50 rows: no scroll, indicator gone, top reset.
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 100})
	if m.scrollTop != 0 {
		t.Errorf("a fitting resize should reset scrollTop, got %d", m.scrollTop)
	}

	if strings.Contains(out, "j/k scroll") || !strings.Contains(out, "h049") {
		t.Errorf("a fitting resize should show every row without the indicator\n---\n%s", out)
	}

	// Shrink again: viewport re-engages from the top.
	m, out = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	if m.scrollTop != 0 || !strings.Contains(out, "h000") {
		t.Errorf(
			"shrinking should re-engage the viewport at the top (scrollTop=%d)\n---\n%s",
			m.scrollTop,
			out,
		)
	}
}

// A reload that shrinks the target set while scrolled near the bottom must clamp
// scrollTop back into range without panicking.
func TestReloadShrinkClampsScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deadman.conf")

	var big strings.Builder
	for i := range 50 {
		fmt.Fprintf(&big, "h%03d 10.0.0.%d\n", i, i+1)
	}

	err := os.WriteFile(path, []byte(big.String()), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	m, err := New(manySpecs(50), Options{Scale: 10, ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}

	// Overflow and scroll to the bottom.
	m, _ = drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 20},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}},
	)

	// Reload a 3-row config: the new set fits, so scrollTop clamps to 0.
	err = os.WriteFile(path, []byte("r0 10.1.0.1\nr1 10.1.0.2\nr2 10.1.0.3\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	m, out := drive(t, m, reloadMsg{})

	if m.scrollTop != 0 {
		t.Errorf("reload to a smaller set should clamp scrollTop to 0, got %d", m.scrollTop)
	}

	for _, want := range []string{"r0", "r1", "r2"} {
		if !strings.Contains(out, want) {
			t.Errorf("reloaded rows should all render, missing %q\n---\n%s", want, out)
		}
	}

	if strings.Contains(out, "j/k scroll") {
		t.Errorf("the reloaded 3-row set fits and must not show the indicator\n---\n%s", out)
	}
}

// When scrolled, targetLine must receive the absolute row index so the probe
// arrow lands on the right row (arrowFor reads m.arrowIdx by absolute index).
func TestArrowUsesAbsoluteIndexWhenScrolled(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10}) // sync mode: a single arrow.
	if err != nil {
		t.Fatal(err)
	}

	// Mark row 40 as the one being probed, then scroll so it is inside the window.
	_, out := drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 20},
		pingStartMsg{idx: 40, gen: 0},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}},
	)

	if n := strings.Count(out, arrow); n != 1 {
		t.Fatalf("sync mode should show exactly one arrow, got %d\n---\n%s", n, out)
	}

	if ln := lineWith(out, arrow); !strings.Contains(ln, "h040") {
		t.Errorf("the arrow must sit on the probed row h040, got %q\n---\n%s", ln, out)
	}
}

// With a width but no height yet (height 0), the whole list renders without a
// viewport and without panicking.
func TestViewportHeightZeroShowsAll(t *testing.T) {
	m, err := New(manySpecs(3), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 0})

	for _, want := range []string{"h000", "h001", "h002"} {
		if !strings.Contains(out, want) {
			t.Errorf("height 0 should still render every row, missing %q\n---\n%s", want, out)
		}
	}

	if strings.Contains(out, "j/k scroll") {
		t.Errorf("no viewport without a height\n---\n%s", out)
	}
}

// indexOfLine returns the index of the first rendered line containing sub, or -1.
func indexOfLine(out, sub string) int {
	i := 0

	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, sub) {
			return i
		}

		i++
	}

	return -1
}

// fixedHeaderLines must equal the number of lines View actually renders before the
// first data row; otherwise the row window would overlap or gap the header. This
// ties the bare "5" constant to the real render (no warnings -> 5, +1 per warning).
func TestFixedHeaderLinesMatchesRender(t *testing.T) {
	// No warnings: title, host info, keys, blank, header = 5 lines before row 0.
	clean, err := New(manySpecs(3), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	clean, out := drive(t, clean, tea.WindowSizeMsg{Width: 120, Height: 40})
	if got, want := indexOfLine(out, "h000"), clean.fixedHeaderLines(); got != want {
		t.Errorf("first data row at line %d, fixedHeaderLines() = %d\n---\n%s", got, want, out)
	}

	if got := clean.fixedHeaderLines(); got != 5 {
		t.Errorf("no warnings: fixedHeaderLines() = %d, want 5", got)
	}

	// A startup warning adds exactly one fixed line above the rows.
	warned, err := New([]config.TargetSpec{{
		Name:    "Cloudflare",
		Addr:    "via",
		Relay:   map[string]string{"nexthop": "10.98.38.9"},
		Dropped: []string{"MGMT", "1.1.1.1"},
	}}, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	warned, out = drive(t, warned, tea.WindowSizeMsg{Width: 120, Height: 40})
	// Match the VIA column (unique to the data row; the warning line also names the host).
	if got, want := indexOfLine(out, "nexthop 10.98.38.9"), warned.fixedHeaderLines(); got != want {
		t.Errorf(
			"with a warning: first data row at %d, fixedHeaderLines() = %d\n---\n%s",
			got,
			want,
			out,
		)
	}

	if got := warned.fixedHeaderLines(); got != 6 {
		t.Errorf("one warning: fixedHeaderLines() = %d, want 6", got)
	}
}

// PgDown pages forward and the Home/End aliases jump to the ends, mirroring
// PgUp/g/G. Guards the bubbletea key-string mapping for the documented keys.
func TestScrollPageDownAndAliases(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})

	// PgDown from the top advances the window past the first page.
	_, out := drive(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if strings.Contains(out, "h000") {
		t.Errorf("PgDown should advance past the top\n---\n%s", out)
	}

	// End -> bottom.
	mEnd, out := drive(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(out, "h049") || !strings.Contains(out, "-50/50]") {
		t.Errorf("End should jump to the bottom\n---\n%s", out)
	}

	// Home from the bottom returns to the top.
	_, out = drive(t, mEnd, tea.KeyMsg{Type: tea.KeyHome})
	if !strings.Contains(out, "h000") || strings.Contains(out, "h049") {
		t.Errorf("Home should return to the top\n---\n%s", out)
	}
}

// At the boundary where the terminal is exactly tall enough for the fixed header
// (height == fixedHeaderLines), there is no room for any data row. The viewport must
// render zero rows rather than forcing one and emitting height+1 lines, which would
// push the title off the top via Bubble Tea's top-drop.
func TestViewportNoRoomForRows(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	// Size once to compute the fixed-header height (5 with no warnings), then shrink
	// the terminal to exactly that.
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	hl := m.fixedHeaderLines()

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: hl})

	if lines := strings.Count(out, "\n") + 1; lines > hl {
		t.Errorf(
			"height==fixedHeaderLines(%d): rendered %d lines (overflow)\n---\n%s",
			hl,
			lines,
			out,
		)
	}

	if !strings.Contains(out, "Dead Man") || !strings.Contains(out, "HOSTNAME") {
		t.Errorf("the fixed header must stay fully on screen at the boundary\n---\n%s", out)
	}

	if strings.Contains(out, "h000") {
		t.Errorf("no room for any data row at the boundary, but one rendered\n---\n%s", out)
	}
}

// The whole point of the viewport: View must never emit more lines than the
// terminal has, so the fixed header is never pushed off-screen. Bubble Tea's
// renderer truncates each line to the terminal width (it does not wrap), so one
// logical line is one screen row regardless of width — making this logical line
// count a valid physical-overflow check at any width, narrow or wide. Height 5
// is the boundary == fixedHeaderLines (no warnings); the rest leave room for rows.
func TestViewFitsTerminalHeight(t *testing.T) {
	m, err := New(manySpecs(50), Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{80, 120, 200} {
		for _, height := range []int{5, 10, 12, 20, 25, 100} {
			_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: height})

			if lines := strings.Count(out, "\n") + 1; lines > height {
				t.Errorf("%dx%d: rendered %d lines (overflow)\n---\n%s", width, height, lines, out)
			}
		}
	}

	// Eyeball sample for `go test -v`: the fixed header plus a scrolled window.
	_, sample := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	t.Logf("sample render (120x20, 50 targets):\n%s", sample)
}
