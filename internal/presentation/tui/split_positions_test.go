package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func positionedSpecs() []config.Line {
	return []config.Line{
		config.ColumnBreak{Label: "LAN"},
		config.Target{Name: "switch-1", Addr: "192.0.2.1"},
		config.Target{Name: "switch-2", Addr: "192.0.2.2"},
		config.Target{Name: "switch-3", Addr: "192.0.2.3"},
		config.ColumnBreak{Label: "WAN"},
		config.Target{Name: "uplink", Addr: "198.51.100.1"},
		config.ColumnBreak{Label: "Servers"},
		config.Target{Name: "server", Addr: "203.0.113.1"},
	}
}

func TestExplicitSplitPositions(t *testing.T) {
	m := newModel(t, positionedSpecs(), testOptions{Cols: 2})
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 400, Height: 20})

	lines := strings.Split(out, "\n")[m.fixedHeaderLines():]
	if m.effectiveCols() != 2 || len(lines) != 4 || strings.Count(out, "HOSTNAME") != 2 {
		t.Fatalf("want three sections stacked into two columns: %q", out)
	}

	for row, want := range [][2]string{
		{"LAN", "WAN"},
		{"switch-1", "uplink"},
		{"switch-2", "Servers"},
		{"switch-3", "server"},
	} {
		cells := strings.Split(lines[row], colGutter)
		if len(cells) != 2 {
			t.Fatalf("row %d has no split: %q", row, lines[row])
		}

		for column, text := range want {
			if (text == "" && strings.TrimSpace(cells[column]) != "") ||
				!strings.Contains(cells[column], text) {
				t.Errorf("row %d column %d = %q, want %q", row, column, cells[column], text)
			}
		}
	}

	assertNoLineExceedsWidth(t, out, m.width)
}

func TestGuideCountDoesNotLimitSplitCount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		positions []int
		label     string
	}{
		{"one bare guide", []int{9}, ""},
		{"one named guide", []int{9}, "Group"},
		{"leading description", []int{0}, "Group"},
		{"several guides", []int{0, 3, 9}, "Group"},
		{"empty groups", []int{0, 0, 3, 9, 12, 12}, "Group"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := manySpecs(12)

			var lines []config.Line

			for index := 0; index <= len(specs); index++ {
				for _, position := range tc.positions {
					if position == index {
						lines = append(lines, config.ColumnBreak{Label: tc.label})
					}
				}

				if index < len(specs) {
					lines = append(lines, specs[index])
				}
			}

			m := newModel(t, lines, testOptions{})
			m, out := drive(t, m, tea.WindowSizeMsg{Width: 2000, Height: 80})

			for requested := 1; requested <= 8; requested++ {
				if m.cols != requested || m.effectiveCols() != requested ||
					strings.Count(out, "HOSTNAME") != requested {
					t.Fatalf("guides limited split %d: %q", requested, out)
				}

				for index := range specs {
					if got := strings.Count(out, manyName(index)); got != 1 {
						t.Errorf("split %d shows %s %d times", requested, manyName(index), got)
					}
				}

				m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
			}
		})
	}
}

func TestAdditionalColumnsPreserveGuidedStarts(t *testing.T) {
	specs := manySpecs(12)
	lines := append([]config.Line{config.ColumnBreak{Label: "LAN"}}, specs[:9]...)
	lines = append(lines, config.ColumnBreak{Label: "WAN"})
	lines = append(lines, specs[9:]...)
	m := newModel(t, lines, testOptions{Cols: 4})
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 1000, Height: 40})
	top := strings.Split(strings.Split(out, "\n")[m.fixedHeaderLines()], colGutter)

	if len(top) != 4 || !strings.Contains(top[0], "LAN") || !strings.Contains(top[3], "WAN") ||
		!strings.Contains(top[1], manyName(3)) || !strings.Contains(top[2], manyName(6)) {
		t.Fatalf("automatic columns ignored the guides or left empty columns: %q", out)
	}
}

// Sections that do not fit side by side share columns as whole sections, so
// every description stays on the first screen instead of continuing far below
// a long section.
func TestFewerColumnsStackWholeSections(t *testing.T) {
	specs := manySpecs(76)
	lines := slices.Concat(
		[]config.Line{config.ColumnBreak{Label: "LAN"}}, specs[:60],
		[]config.Line{config.ColumnBreak{Label: "WAN"}}, specs[60:70],
		[]config.Line{config.ColumnBreak{Label: "NOC"}}, specs[70:],
	)

	m := newModel(t, lines, testOptions{Cols: 3})
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 2000, Height: 100})
	width := 3*m.minColumnWidth() + 2*colGutterWidth - 1

	m, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: 100})
	if m.effectiveCols() != 2 || m.grid.rows != 61 || strings.Count(out, "HOSTNAME") != 2 {
		t.Fatalf("want LAN alone and WAN with NOC beside it: %q", out)
	}

	for index := range specs {
		if got := strings.Count(out, manyName(index)); got != 1 {
			t.Errorf("%s is shown %d times", manyName(index), got)
		}
	}

	m, out = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 30})
	if m.scrollTop != 0 || !strings.Contains(out, "[1-") {
		t.Fatalf("want the long section to scroll: %q", out)
	}

	for label, column := range map[string]int{"LAN": 0, "WAN": 1, "NOC": 1} {
		cells := strings.Split(lineWith(out, label), colGutter)
		if len(cells) != 2 || strings.TrimSpace(cells[column]) != label {
			t.Errorf("description %s is not at the top of column %d: %q", label, column+1, out)
		}
	}

	assertNoLineExceedsWidth(t, out, width)
}

func TestSplitDescriptionsWithoutSplitting(t *testing.T) {
	m := newModel(t, positionedSpecs(), testOptions{})
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 180, Height: 20})

	lines := strings.Split(out, "\n")[m.fixedHeaderLines():]
	if m.effectiveCols() != 1 || strings.Count(out, "HOSTNAME") != 1 || len(lines) != 8 {
		t.Fatalf("want a single list with descriptions: %q", out)
	}

	for index, label := range map[int]string{0: "LAN", 4: "WAN", 6: "Servers"} {
		if strings.TrimSpace(lines[index]) != label {
			t.Errorf("description = %q, want plain text %q", lines[index], label)
		}
	}

	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyRight, tea.KeyLeft} {
		_, after := drive(t, m, tea.KeyMsg{Type: key})
		if after != out {
			t.Errorf("unexpected pane selection from %v", key)
		}
	}
}

func TestBareSplitPositionsUseNoDisplayRows(t *testing.T) {
	m := newModel(t, []config.Line{
		config.Target{Name: "left", Addr: "192.0.2.1"},
		config.ColumnBreak{},
		config.Target{Name: "right", Addr: "192.0.2.2"},
	}, testOptions{Cols: 2})
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 400, Height: 20})

	lines := strings.Split(out, "\n")[m.fixedHeaderLines():]
	if len(lines) != 1 || !strings.Contains(lines[0], "left") ||
		!strings.Contains(lines[0], "right") {
		t.Fatalf("bare break added an unwanted display row: %q", out)
	}

	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if m.effectiveCols() != 1 || len(strings.Split(out, "\n")) != m.fixedHeaderLines()+2 {
		t.Fatalf("reducing split did not restore a continuous list: %q", out)
	}
}

func TestSplitPositionsUseSharedScrollAndColumnKeys(t *testing.T) {
	m := newModel(t, positionedSpecs(), testOptions{Cols: 2})

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 400, Height: 8})
	if !strings.Contains(out, "[1-2/4]") || strings.Contains(out, "server ") {
		t.Fatalf("incorrect initial scroll window: %q", out)
	}

	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(out, "[3-4/4]") || !strings.Contains(out, "server") ||
		strings.Contains(out, "uplink") {
		t.Fatalf("scroll did not reach the end of the stacked columns: %q", out)
	}

	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if m.effectiveCols() != 3 {
		t.Fatalf("increasing split did not use three columns: %d", m.effectiveCols())
	}

	// Four columns divide LAN, so the list fits and the shared scroll returns to the top.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if m.effectiveCols() != 4 || m.scrollTop != 0 || !strings.Contains(out, "server") ||
		!strings.Contains(out, "switch-1") {
		t.Fatalf("increasing split did not use four columns and clamp shared scroll: %q", out)
	}

	m, out = drive(t, m, tea.WindowSizeMsg{Width: m.minColumnWidth(), Height: 20})
	if m.effectiveCols() != 1 || m.scrollTop != 0 || !strings.Contains(out, "server") {
		t.Fatalf("narrow width hid a section instead of continuing vertically: %q", out)
	}
}

func TestSplitPositionsReloadPreservesResultIndices(t *testing.T) {
	src := sourceOf(positionedSpecs(), testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{Cols: 2})
	m, _ = drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 400, Height: 20},
		answer{row: 1, res: probe.SuccessResult(1)},
	)
	before := snapshotAt(t, m, 1)
	src.cfg.Lines = []config.Line{
		config.Blank{},
		config.Target{Name: "switch-1", Addr: "192.0.2.1"},
		config.ColumnBreak{Label: "Renamed"},
		config.Target{Name: "new", Addr: "203.0.113.2"},
	}

	m, out := drive(t, m, reloadMsg{})
	if !strings.Contains(out, "Renamed") || strings.Contains(out, "WAN") {
		t.Fatalf("split boundaries or descriptions were not reloaded: %q", out)
	}

	after := snapshotAt(t, m, 1)
	if after.ID != before.ID || after.Snt != before.Snt || after.Len() != before.Len() {
		t.Fatal("changing split boundaries changed target identity or history")
	}

	src.cfg.Lines = []config.Line{config.Target{Name: "switch-1", Addr: "192.0.2.1"}}

	m, out = drive(t, m, reloadMsg{})
	if len(m.sections) != 0 || strings.Contains(out, "Renamed") ||
		m.fixedHeaderLines() != nonWarnLines {
		t.Fatalf("removing split positions did not restore automatic splitting: %q", out)
	}
}

func TestSplitPositionProbeIndicators(t *testing.T) {
	for _, async := range []bool{false, true} {
		m := newModel(t, []config.Line{
			config.ColumnBreak{Label: "LAN"},
			config.Blank{},
			config.Target{Name: "local", Addr: "192.0.2.1"},
			config.ColumnBreak{},
			config.Malformed{Name: "invalid", Addr: "198.51.100.2", Problem: "unknown probe"},
			config.Target{Name: "remote", Addr: "198.51.100.1"},
		}, testOptions{Async: async, Cols: 2})

		m, out := drive(t, m, tea.WindowSizeMsg{Width: 400, Height: 15}, until(func(m Model) bool {
			if async {
				return len(m.inflight) == 6 && m.inflight[2] && m.inflight[5]
			}

			return m.arrowIdx == 2
		}))
		if m.arrowFor(2) != arrow || m.arrowFor(4) != rear || !strings.Contains(out, "invalid") {
			t.Fatalf("incorrect result indices, async=%v: %q", async, out)
		}

		m, out = drive(t, m, answer{row: 2, res: probe.SuccessResult(1)}, until(func(m Model) bool {
			return m.arrowFor(5) == arrow
		}))
		if snapshotAt(t, m, 2).Snt != 1 || snapshotAt(t, m, 5).Snt != 0 {
			t.Fatalf("result went to another section, async=%v: %q", async, out)
		}
	}
}

func TestSplitPositionsFollowGrowingStatWidths(t *testing.T) {
	m := newModel(t, positionedSpecs(), testOptions{Cols: 2})
	m = m.recalcWidths()
	width := 2*m.minColumnWidth() + colGutterWidth
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 20})

	if m.effectiveCols() != 2 {
		t.Fatal("expected two columns before the statistics grew")
	}

	m, out := drive(t, m, answer{row: 1, res: probe.SuccessResult(100000)})
	if m.effectiveCols() != 1 || strings.Count(out, "HOSTNAME") != 1 ||
		!strings.Contains(out, "server") {
		t.Fatalf("growing statistics did not restack every section into one column: %q", out)
	}

	// Resetting the statistics restores the original width and two-column placement.
	m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.effectiveCols() != 2 || strings.Count(out, "HOSTNAME") != 2 ||
		!strings.Contains(out, "server") {
		t.Fatalf("reset did not restore both columns: %q", out)
	}
}

func TestSplitPositionsFitSmallScreens(t *testing.T) {
	withColor(t, termenv.TrueColor, true)

	for _, specs := range [][]config.Line{
		positionedSpecs(),
		{config.ColumnBreak{}},
		{config.ColumnBreak{}, config.ColumnBreak{}},
		{config.ColumnBreak{Label: "\u4f1a\u5834 LAN"}, config.Blank{}},
	} {
		m := newModel(t, specs, testOptions{Cols: 2})

		for _, width := range []int{1, 20, 200, 600} {
			for _, height := range []int{1, 5, 6, 7, 8, 20} {
				var out string

				m, out = drive(t, m, tea.WindowSizeMsg{Width: width, Height: height})
				if len(strings.Split(out, "\n")) > height {
					t.Errorf("view exceeds height %d: %q", height, out)
				}

				assertNoLineExceedsWidth(t, out, width)
			}
		}
	}
}

func TestStackSectionsBalancesWholeSections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes []int
		cols  int
		want  []int
	}{
		{"long first", []int{61, 11, 6}, 2, []int{61, 17}},
		{"long last", []int{6, 11, 61}, 2, []int{17, 61}},
		{"equal pairs", []int{5, 5, 5, 5}, 2, []int{10, 10}},
		{"odd count", []int{3, 3, 3}, 2, []int{6, 3}},
		{"no header-only column", []int{10, 1, 1, 1}, 3, []int{10, 2, 1}},
		{"empty sections", []int{0, 4, 0, 4}, 2, []int{4, 4}},
		{"one column", []int{2, 3, 4}, 1, []int{9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sections := make([]splitSection, len(tc.sizes))
			next := 0

			for index, size := range tc.sizes {
				for range size {
					sections[index].indices = append(sections[index].indices, next)
					next++
				}
			}

			columns := stackSections(sections, tc.cols)

			got := make([]int, len(columns))
			order := []int{}

			for index, column := range columns {
				got[index] = len(column.indices)
				order = append(order, column.indices...)
			}

			if !slices.Equal(got, tc.want) {
				t.Errorf("column heights = %v, want %v", got, tc.want)
			}

			for index, value := range order {
				if value != index {
					t.Fatalf("rows reordered or lost: %v", order)
				}
			}

			if len(order) != next {
				t.Fatalf("shown %d rows, want %d", len(order), next)
			}
		})
	}
}
