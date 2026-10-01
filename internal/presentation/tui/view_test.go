package tui

import (
	"fmt"
	"math"
	"net/netip"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// drive renders the model after applying a sequence of messages. An answer is delivered
// through the session (see program.answer); a reload is applied at once, its reread run in
// place; the commands any other message returns run in the model's program.
func drive(t *testing.T, m Model, msgs ...tea.Msg) (Model, string) {
	t.Helper()

	prog := programOf(t, m)

	for _, msg := range msgs {
		switch msg := msg.(type) {
		case answer:
			m = prog.answer(t, m, msg)
		case until:
			m = prog.pump(t, m, msg)
		default:
			next, cmd := m.Update(msg)
			if _, reload := msg.(reloadMsg); reload && cmd != nil {
				next, cmd = next.Update(cmd())
			}

			m = asModel(t, next)

			prog.run(cmd)
		}
	}

	return m, m.View()
}

// fillWideCounts fills every target's history with full successes and forces 6-digit
// Snt/Loss, so the stat columns outgrow their 5-wide header and the layout must shrink
// the result bar.
func fillWideCounts(m Model) {
	for i, r := range m.rows() {
		row, ok := r.(monitoring.Monitored)
		if !ok {
			continue
		}

		fixture := monitor.NewTarget(row.Target.ID, row.Target.Name, row.Target.Addr)
		for range 300 {
			fixture.Consume(probe.SuccessResult(5))
		}

		row.Target = fixture.Snapshot()
		row.Target.Snt = 123456
		row.Target.Loss = 654321
		m.lines[i] = row // the rows share the caller's backing array.
	}
}

// assertNoLineExceedsWidth fails if any rendered line is wider than width.
func assertNoLineExceedsWidth(t *testing.T, out string, width int) {
	t.Helper()

	for ln := range strings.SplitSeq(out, "\n") {
		if w := lipgloss.Width(ln); w > width {
			t.Errorf("rendered line exceeds terminal width: %d > %d\n%q", w, width, ln)
		}
	}
}

func TestViewRendersTargetsAndSeparator(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "host1", Addr: "1.2.3.4", Params: probe.Direct{}},
		config.Separator{},
		config.Separator{Label: "Congre Routers / Switches"},
		config.Separator{Label: "\u793e\u5185\u7db2 AP"},
		config.Target{Name: "host2", Addr: "5.6.7.8", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		success(5),
	)

	for _, want := range []string{
		"Dead Man", "HOSTNAME", "ADDRESS", "LOSS",
		"MIN", "MAX", "JIT", "FAIL", // the added statistics columns.
		"host1", "1.2.3.4", "host2", "5.6.7.8", "▁",
		"--- Congre Routers / Switches ---", "--- \u793e\u5185\u7db2 AP ---",
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

func TestSeparatorCellWidth(t *testing.T) {
	m := Model{layout: layout{hostW: 9}}

	for _, c := range []struct {
		label, want string
		width       int
	}{
		{width: 20, want: "   --------------"},
		{width: 20, label: "LAN", want: "   --- LAN ------"},
		{width: 20, label: "\u793e\u5185\u7db2", want: "   --- \u793e\u5185\u7db2 ---"},
		{width: 0, label: "LAN", want: ""},
		{width: 1, label: "LAN", want: " "},
		{width: 6, label: "LAN", want: "   "},
		{width: 10, label: "LAN", want: "   --- "},
	} {
		if got := m.separatorCell(c.width, c.label); got != c.want {
			t.Errorf("separatorCell(%d, %q) = %q, want %q", c.width, c.label, got, c.want)
		}
	}

	for _, hostWidth := range []int{9, 16, 20} {
		m.hostW = hostWidth

		for _, label := range []string{"", "Congre Routers / Switches", strings.Repeat("\u793e\u5185\u7db2", 100)} {
			for width := range 150 {
				got := m.separatorCell(width, label)
				assertNoLineExceedsWidth(t, got, width)

				if w := lipgloss.Width(got); width >= 6 && w > width-len(arrow) {
					t.Errorf(
						"separator with label %q exceeds content width: %d > %d",
						label,
						w,
						width-len(arrow),
					)
				}
			}
		}
	}
}

func TestSeparatorLabelFollowsHostnameWidth(t *testing.T) {
	src := sourceOf([]config.Line{
		config.Separator{Label: "Group"},
		config.Target{Name: "short", Addr: "192.0.2.1", Params: probe.Direct{}},
	}, testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{})
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, c := range []struct {
		name  string
		start int
	}{
		{name: "short", start: 7},
		{name: "abcdefghijklmnop", start: 11},
		{name: strings.Repeat("ル", 8), start: 11},
		{name: strings.Repeat("x", 30), start: 13},
		{name: "short", start: 7},
	} {
		src.cfg.Lines[1] = config.Target{Name: c.name, Addr: "192.0.2.1", Params: probe.Direct{}}
		m, _ = drive(t, m, reloadMsg{})

		if row := m.rowLine(0, 120); strings.Index(row, "Group") != c.start {
			t.Errorf(
				"hostname %q: label position = %d, want %d: %q",
				c.name,
				strings.Index(row, "Group"),
				c.start,
				row,
			)
		}
	}

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	if row := m.rowLine(0, 80); strings.Index(row, "Group") != 7 {
		t.Errorf("resized separator label shifted: %q", row)
	}

	src.cfg.Lines[1] = config.Target{
		Name:   "abcdefghijklmnop",
		Addr:   "192.0.2.1",
		Params: probe.Direct{},
	}

	m, _ = drive(t, m, reloadMsg{}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if row := m.rowLine(0, 80); strings.Index(row, "Group") != 7 {
		t.Errorf("hidden HOSTNAME should use the default separator prefix: %q", row)
	}

	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if row := m.rowLine(0, 80); strings.Index(row, "Group") != 11 {
		t.Errorf("restored HOSTNAME should restore the midpoint alignment: %q", row)
	}
}

func TestViaColumnAndToggle(t *testing.T) {
	specs := []config.Line{
		config.Target{
			Name:   "cf",
			Addr:   "1.1.1.1",
			Params: probe.Nexthop{Gateway: netip.MustParseAddr("10.98.38.9")},
		},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

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
	specs := []config.Line{
		config.Target{
			Name:   "cf",
			Addr:   "1.1.1.1",
			Params: probe.Nexthop{Gateway: netip.MustParseAddr("10.98.38.9")},
		},
	}

	m := newModel(t, specs, testOptions{Scale: 10, Columns: map[string]bool{"VIA": false}})

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(out, "VIA") || strings.Contains(out, "nexthop 10.98.38.9") {
		t.Errorf("columns VIA=off should hide the VIA column at startup\n---\n%s", out)
	}
}

// The 'h' and 'a' keys hide and restore the structural HOSTNAME and ADDRESS columns,
// and the "columns" directive can hide them at startup — bringing them to VIA's level
// (every column but RESULT is toggleable).
func TestHostAddrColumnToggle(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "alpha", Addr: "203.0.113.7", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

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
	specs := []config.Line{
		config.Target{Name: "beta", Addr: "198.51.100.9", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10, Columns: map[string]bool{"ADDRESS": false}})

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
	specs := []config.Line{
		config.Target{
			Name:   "host-longname",
			Addr:   "203.0.113.45",
			Params: probe.Nexthop{Gateway: netip.MustParseAddr("10.0.0.1")},
		},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

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

// strayTokensLines is how an unquoted name with spaces parses — "Cloudflare via MGMT
// 1.1.1.1 probe=nexthop nexthop=10.98.38.9" — and strayTokensNotes the parser's note on
// the words it dropped.
var (
	strayTokensLines = []config.Line{config.Target{
		Name:   "Cloudflare",
		Addr:   "via",
		Params: probe.Nexthop{Gateway: netip.MustParseAddr("10.98.38.9")},
	}}
	strayTokensNotes = []config.Note{
		{Name: "Cloudflare", Addr: "via", Dropped: []string{"MGMT", "1.1.1.1"}},
	}
)

func TestParseWarningShown(t *testing.T) {
	// A name with spaces leaves stray tokens; the startup warning surfaces them.
	m := newNotedModel(t, strayTokensLines, strayTokensNotes, testOptions{Scale: 10})

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, want := range []string{"ignored stray tokens", "MGMT 1.1.1.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing parse warning %q\n---\n%s", want, out)
		}
	}
}

// A target that cannot be built never enters the session: its row shows its diagnostic and
// a static failure, while the other rows keep being monitored.
func TestRejectedTargetShowsStaticFailure(t *testing.T) {
	m := newModel(
		t,
		[]config.Line{
			config.Malformed{Name: "bad", Addr: "192.0.2.1", Problem: "source unsupported"},
		},
		testOptions{},
	)
	if len(m.rows()) != 1 || !isRejected(m.rows()[0]) {
		t.Fatal("rejected target entered session")
	}

	_, view := drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	if !strings.Contains(view, "source unsupported") || !strings.Contains(view, "X") {
		t.Fatalf("missing static diagnostic: %s", view)
	}
}

func TestUnterminatedQuoteWarningShown(t *testing.T) {
	m := newNotedModel(
		t,
		[]config.Line{config.Target{Name: "host", Addr: "1.2.3.4", Params: probe.Direct{}}},
		[]config.Note{{Name: "host", Addr: "1.2.3.4", UnterminatedQuote: true}},
		testOptions{Scale: 10},
	)

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "unterminated quote") {
		t.Errorf("view missing unterminated-quote warning\n---\n%s", out)
	}
}

// TestICMPPrivilegeWarningRenders confirms the warning reaches the screen: an
// unprivileged host with a direct target surfaces the (always-visible, front-of-line)
// problem statement in the header, not just in the warning slice. The remedy's
// on-screen visibility is guarded separately by the application's
// TestICMPPrivilegeWarnings.
func TestICMPPrivilegeWarningRenders(t *testing.T) {
	o := testOptions{Scale: 10}
	src := sourceOf([]config.Line{config.Target{Name: "a", Addr: "8.8.8.8"}}, o)
	m := openModel(t, testService(stubHost{noDirectICMP: true}, src), o)

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "native ICMP unavailable") {
		t.Errorf("view should surface the ICMP-privilege warning\n---\n%s", out)
	}
}

func TestMinMaxToggleHidesColumns(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "host1", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		success(5),
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
	specs := []config.Line{
		config.Target{Name: "host1", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(
		t,
		specs,
		testOptions{Scale: 10, Columns: map[string]bool{"MIN": false, "MAX": false}},
	)

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		success(5),
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

// Below fixedHeaderLines the header alone would overflow the terminal. View must
// clamp its output to the height and keep the title (line 0) on screen rather than
// let Bubble Tea's top-drop eat it.
func TestViewClampsTinyTerminal(t *testing.T) {
	m := newModel(t, manySpecs(5), testOptions{Scale: 10})

	for _, height := range []int{1, 2, 3, 4} {
		_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: height})

		if lines := strings.Count(out, "\n") + 1; lines > height {
			t.Errorf("height %d: rendered %d lines (overflow)\n---\n%s", height, lines, out)
		}

		if !strings.Contains(out, "Dead Man") {
			t.Errorf("height %d: title must survive the clamp\n---\n%s", height, out)
		}
	}
}

func TestViewEmptyBeforeSize(t *testing.T) {
	m := newModel(
		t,
		[]config.Line{
			config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
		},
		testOptions{Scale: 10},
	)

	if out := m.View(); out != "" {
		t.Errorf("expected empty view before WindowSizeMsg, got %q", out)
	}
}

func TestRefreshKeyResetsStats(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	m, _ = drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 100, Height: 20},
		success(5),
	)
	if snapshotAt(t, m, 0).Snt != 1 {
		t.Fatalf("Snt = %d, want 1", snapshotAt(t, m, 0).Snt)
	}

	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if snapshotAt(t, m, 0).Snt != 0 {
		t.Errorf("after 'r', Snt = %d, want 0", snapshotAt(t, m, 0).Snt)
	}
}

func TestPrecisionCycle(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		success(5),
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
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

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

	// down past the bottom clamps at the sub-ms floor (scaleSteps[0]). Pressing Down
	// len(scaleSteps) times reaches the floor from any rung, so the count and the
	// expected floor label both track the ladder instead of hardcoded literals.
	floor := scaleLabel(scaleSteps[0])

	downs := make([]tea.Msg, len(scaleSteps))
	for i := range downs {
		downs[i] = tea.KeyMsg{Type: tea.KeyDown}
	}

	m, out = drive(t, m, downs...)
	if !strings.Contains(out, "RTT Scale "+floor+"ms") {
		t.Errorf("down past the bottom should clamp at %sms\n---\n%s", floor, out)
	}

	// further down past the floor stays clamped at the floor.
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(out, "RTT Scale "+floor+"ms") {
		t.Errorf("further down past the floor must stay at %sms\n---\n%s", floor, out)
	}
}

// scaleUp/scaleDown move through the preset ladder, but the live scale may be a
// free-form CLI/config value off the ladder. The key invariant: Up (coarser) must
// never decrease the scale and Down (finer) must never increase it, even above the
// top rung or below the bottom one.
func TestScaleLadderBounds(t *testing.T) {
	cases := []struct {
		name           string
		cur            float64
		wantUp, wantDn float64
	}{
		{"within ladder", 10, 20, 5},
		{"off-ladder below top", 7, 10, 5},
		{"at top rung", 100, 100, 50},
		{"above top rung stays put on up", 1000, 1000, 100}, // the Bug-1 regression guard.
		{"at bottom rung", 0.01, 0.02, 0.01},
		{"off-ladder near bottom", 3, 5, 2},
		{"sub-ms mid", 0.1, 0.2, 0.05},
		{"below bottom holds", 0.005, 0.01, 0.005},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scaleUp(c.cur); got != c.wantUp {
				t.Errorf("scaleUp(%g) = %g, want %g", c.cur, got, c.wantUp)
			}

			if got := scaleDown(c.cur); got != c.wantDn {
				t.Errorf("scaleDown(%g) = %g, want %g", c.cur, got, c.wantDn)
			}
		})
	}
}

func TestScaleRebucketsExistingBar(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	// One probe at RTT 15 renders ▂ at scale 10.
	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		success(15),
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

// TestLogModeKey cycles the 'l' key (linear -> base e -> base e² -> linear), asserting
// the footer relabels to floor mode and surfaces the factor up front (xe / xe2) and
// wraps back. This is the regression guard for the 'l' wiring and the logFactors lookup.
func TestLogModeKey(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 40})
	if !strings.Contains(out, "RTT Scale 10ms") {
		t.Errorf("linear start should show 'RTT Scale 10ms'\n---\n%s", out)
	}

	// 'l' -> base e: the footer switches to floor wording and the xe factor.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if !strings.Contains(out, "RTT floor 10ms xe.") {
		t.Errorf("after l: want 'RTT floor 10ms xe'\n---\n%s", out)
	}

	// 'l' -> base e²: the xe2 factor (the trailing '.' keeps this from matching xe).
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if !strings.Contains(out, "RTT floor 10ms xe2.") {
		t.Errorf("after l,l: want xe2\n---\n%s", out)
	}

	// 'l' wraps back to linear.
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if !strings.Contains(out, "RTT Scale 10ms") {
		t.Errorf("after l×3: should wrap back to linear\n---\n%s", out)
	}
}

// TestLogModeRebucketsBar confirms 'l' re-buckets the on-screen bar through the View
// path (targetLine passing the selected LnBase to resultbar.Glyph): the same stored
// RTT 50 renders ▆ on the linear scale 10 but ▂ in log ×e (ln(50/10)≈1.6, band 1).
func TestLogModeRebucketsBar(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10})

	m, out := drive(t, m,
		tea.WindowSizeMsg{Width: 200, Height: 40},
		success(50),
	)
	if !strings.Contains(out, "▆") {
		t.Errorf("RTT 50 at linear scale 10 should render ▆\n---\n%s", out)
	}

	// switch to log ×e: 50ms re-buckets from ▆ to ▂.
	_, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if !strings.Contains(out, "▂") || strings.Contains(out, "▆") {
		t.Errorf("in log ×e, RTT 50 should re-bucket to ▂ not ▆\n---\n%s", out)
	}
}

func TestPrecisionFromConfigAtStartup(t *testing.T) {
	specs := []config.Line{config.Target{Name: "h", Addr: "1.2.3.4", Params:

	// A config "precision ms.1" reaches the model through the session's config read and
	// must take effect at the first paint, before any key press.
	probe.Direct{}}}

	m := newModel(t, specs, testOptions{Scale: 10, Precision: "ms.1"})

	_, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		success(5),
	)
	if !strings.Contains(out, "(p)recision[ms.1]") || !strings.Contains(out, "5.0") {
		t.Errorf("config precision ms.1 should render one decimal at startup\n---\n%s", out)
	}
}

func TestReloadPreservesScaleAndPrecision(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}
	src := sourceOf(specs, testOptions{})

	m := openModel(t, testService(stubHost{}, src), testOptions{Scale: 10})

	// The edited file hides MIN and carries no scale/precision directive.
	src.cfg.Display.Columns = map[string]bool{"MIN": false}

	// Live: step the scale to 5, cycle precision to ms.1, and switch to log mode.
	m, _ = drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}},
	)

	// Reload reparses the file: columns reset to it (MIN hidden), but the live
	// scale/precision/log-factor are preserved (the documented, intentional asymmetry).
	_, out := drive(t, m, reloadMsg{})

	// The trailing '.' makes this fail if a reload bug advances logIdx 1->2 (xe -> xe2).
	if !strings.Contains(out, "RTT floor 5ms xe.") || !strings.Contains(out, "(p)recision[ms.1]") {
		t.Errorf("reload should preserve the live scale/precision/log-factor\n---\n%s", out)
	}

	if strings.Contains(out, "MIN") {
		t.Errorf("reload should reset columns to the file (MIN hidden)\n---\n%s", out)
	}
}

// manySpecs builds n direct-ICMP targets named h000, h001, … (manyName) so each name
// is a unique, non-overlapping substring for View assertions.
func manySpecs(n int) []config.Line {
	specs := make([]config.Line, n)
	for i := range specs {
		specs[i] = config.Target{
			Name:   manyName(i),
			Addr:   fmt.Sprintf("10.0.0.%d", i+1),
			Params: probe.Direct{},
		}
	}

	return specs
}

// manyName is the name of manySpecs' target i.
func manyName(i int) string { return fmt.Sprintf("h%03d", i) }

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
	m := newModel(t, manySpecs(3), testOptions{Scale: 10})

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10})

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10})

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10})

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
	src := sourceOf(manySpecs(50), testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{Scale: 10})

	// Overflow and scroll to the bottom.
	m, _ = drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 20},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}},
	)

	// Reload a 3-row config: the new set fits, so scrollTop clamps to 0.
	src.cfg = config.Config{Lines: []config.Line{
		config.Target{Name: "r0", Addr: "10.1.0.1", Params: probe.Direct{}},
		config.Target{Name: "r1", Addr: "10.1.0.2", Params: probe.Direct{}},
		config.Target{Name: "r2", Addr: "10.1.0.3", Params: probe.Direct{}},
	}}

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10}) // sync mode: a single arrow.

	// Mark row 40 as the one being probed (as the session reports it, Transition.Probing),
	// then scroll so it is inside the window.
	m.arrowIdx = 40
	_, out := drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 20},
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
	m := newModel(t, manySpecs(3), testOptions{Scale: 10})

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
	clean := newModel(t, manySpecs(3), testOptions{Scale: 10})

	clean, out := drive(t, clean, tea.WindowSizeMsg{Width: 120, Height: 40})
	if got, want := indexOfLine(out, "h000"), clean.fixedHeaderLines(); got != want {
		t.Errorf("first data row at line %d, fixedHeaderLines() = %d\n---\n%s", got, want, out)
	}

	if got := clean.fixedHeaderLines(); got != 5 {
		t.Errorf("no warnings: fixedHeaderLines() = %d, want 5", got)
	}

	// A startup warning adds exactly one fixed line above the rows.
	warned := newNotedModel(t, strayTokensLines, strayTokensNotes, testOptions{Scale: 10})

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10})

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10})

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
	m := newModel(t, manySpecs(50), testOptions{Scale: 10})

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

func TestViewFitsTinyTerminal(t *testing.T) {
	m := newModel(t, manySpecs(10), testOptions{Async: true, Scale: 10})
	m.hostInfo = "From: ＨＯＳＴ (2001:db8::1)"
	m.opts.Version = strings.Repeat("ＶＥＲ", 20)
	m.warnings = []string{strings.Repeat("ＷＡＲＮ", 40)}
	fillWideCounts(m)

	for _, width := range []int{1, 2, 5, 10, 20, 30, 80} {
		for _, height := range []int{1, 2, 4, 6, 10, 20} {
			_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: height})
			assertNoLineExceedsWidth(t, out, width)

			if lines := strings.Count(out, "\n") + 1; lines > height {
				t.Errorf("%dx%d: rendered %d lines", width, height, lines)
			}
		}
	}
}

func TestSpinnerHandlesCounterOverflow(t *testing.T) {
	for _, tick := range []int{-1, -len(spinnerChars), math.MinInt, math.MaxInt} {
		got := spinner(tick)
		if !strings.Contains(spinnerChars, got) {
			t.Errorf("spinner(%d) = %q, not a spinner glyph", tick, got)
		}
	}

	if got := spinner(-1); got != string(spinnerChars[len(spinnerChars)-1]) {
		t.Errorf("spinner(-1) = %q, want last frame", got)
	}
}

// TestFlagWarningsSurface confirms a warning about a command-line flag (an unusable -s)
// is rendered (with the "! " prefix) ahead of the rows, like the per-target startup
// warnings, and that it survives a reload: the rejected flag it describes is still in
// force, so a reload that regenerates the config warnings must re-prepend the flag ones.
func TestFlagWarningsSurface(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h", Addr: "1.2.3.4", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: -1})

	const want = "! -s -1 ignored"

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, want) {
		t.Errorf("a flag warning should render with a '! ' prefix\n---\n%s", out)
	}

	_, out = drive(t, m, reloadMsg{})
	if !strings.Contains(out, want) {
		t.Errorf("a flag warning should survive a reload\n---\n%s", out)
	}
}

func TestHostFactsAreSuppliedByCaller(t *testing.T) {
	for _, c := range []struct {
		opts testOptions
		want string
	}{
		{testOptions{}, "From: unknown"},
		{testOptions{Hostname: "offline"}, "From: offline"},
		{testOptions{Hostname: "local", HostAddress: "192.0.2.1"}, "From: local (192.0.2.1)"},
	} {
		_, out := drive(t, newModel(t, nil, c.opts), tea.WindowSizeMsg{Width: 120, Height: 20})
		if !strings.Contains(out, c.want) {
			t.Fatalf("view = %q, want host line %q", out, c.want)
		}
	}
}
