package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
)

func TestViewBlankRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []config.Line
		want  []string
	}{
		{
			name: "leading consecutive and trailing",
			lines: []config.Line{
				config.Blank{},
				config.Target{Name: "host1", Addr: "192.0.2.1"},
				config.Blank{},
				config.Blank{},
				config.Target{Name: "host2", Addr: "192.0.2.2"},
				config.Blank{},
				config.Blank{},
			},
			want: []string{"", "host1", "", "", "host2", "", ""},
		},
		{
			name:  "only blanks",
			lines: []config.Line{config.Blank{}, config.Blank{}},
			want:  []string{"", ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(t, tc.lines, testOptions{})
			m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			lines := strings.Split(out, "\n")

			start := m.fixedHeaderLines()
			if len(lines) != start+len(tc.want) {
				t.Fatalf("View has %d lines, want %d: %q", len(lines), start+len(tc.want), out)
			}

			for index, want := range tc.want {
				got := lines[start+index]
				if (want == "" && got != "") || !strings.Contains(got, want) {
					t.Errorf("row %d = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func TestViewScrollsBlankRows(t *testing.T) {
	m := newModel(t, []config.Line{
		config.Blank{},
		config.Target{Name: "host1", Addr: "192.0.2.1"},
		config.Blank{},
		config.Target{Name: "host2", Addr: "192.0.2.2"},
		config.Blank{},
	}, testOptions{})
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 7})

	for _, position := range []string{"[1-1/5]", "[5-5/5]"} {
		lines := strings.Split(out, "\n")
		if len(lines) != 7 || lines[m.fixedHeaderLines()] != "" ||
			!strings.Contains(out, position) {
			t.Fatalf("want a visible blank and scroll position %s: %q", position, out)
		}

		m, out = drive(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	}
}

func TestTwoColumnBlankRows(t *testing.T) {
	m := newModel(t, []config.Line{
		config.Target{Name: "host1", Addr: "192.0.2.1"},
		config.Blank{},
		config.Target{Name: "host2", Addr: "192.0.2.2"},
		config.Blank{},
	}, testOptions{Cols: 2})
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 300, Height: 40})
	lines := strings.Split(out, "\n")
	start := m.fixedHeaderLines()

	if strings.Count(out, "HOSTNAME") != 2 || len(lines) != start+2 {
		t.Fatalf("want two columns with two rows each: %q", out)
	}

	blank := strings.Repeat(" ", m.colContentWidth())
	if !strings.Contains(lines[start], "host1") || !strings.Contains(lines[start], "host2") ||
		lines[start+1] != blank+colGutter+blank {
		t.Fatalf("want each column's target followed by a blank: %q", out)
	}

	assertNoLineExceedsWidth(t, out, m.width)
}

func TestViewReloadsSeparatorBlank(t *testing.T) {
	src := sourceOf([]config.Line{
		config.Separator{Label: "First"},
		config.Target{Name: "host1", Addr: "192.0.2.1"},
		config.Separator{Label: "Second"},
		config.Target{Name: "host2", Addr: "192.0.2.2"},
	}, testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{})
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, enabled := range []bool{true, false} {
		src.cfg.Display.SeparatorBlank = enabled
		next, out := drive(t, m, reloadMsg{})
		m = next
		lines := strings.Split(out, "\n")[m.fixedHeaderLines():]

		separator := 2
		if enabled {
			separator = 3
		}

		if len(lines) != separator+2 {
			t.Fatalf("separator_blank=%v: incorrect row count: %q", enabled, out)
		}

		if !strings.Contains(lines[0], "First") ||
			!strings.Contains(lines[separator], "Second") {
			t.Errorf("separator_blank=%v: incorrect separator spacing: %q", enabled, out)
		}

		if enabled && lines[2] != "" {
			t.Errorf("separator_blank=on: missing blank: %q", out)
		}
	}
}
