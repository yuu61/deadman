package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

const (
	sgrUnderline = "\x1b[4m"
	sgrBold      = "\x1b[1m"
)

// failResult builds a failed pingResultMsg for row 0 of m (current generation).
func failResult(m Model) pingResultMsg {
	r := okResult(m, 0)
	r.res = ping.Result{Code: ping.Failed}

	return r
}

// TestRecentFailureMark checks how a row's name shows its state: a target answering
// with a failure still in its bar has its name underlined (the text only, not the
// padding), one not answering is bold instead, and a clean one is plain.
func TestRecentFailureMark(t *testing.T) {
	specs := []config.TargetSpec{{Name: "alpha", Addr: "203.0.113.7", Relay: map[string]string{}}}

	cases := []struct {
		name      string
		results   func(m Model) []tea.Msg
		underline bool
		bold      bool
	}{
		{
			"clean",
			func(m Model) []tea.Msg { return []tea.Msg{okResult(m, 1), okResult(m, 1)} },
			false,
			false,
		},
		{
			"answering_again",
			func(m Model) []tea.Msg { return []tea.Msg{okResult(m, 1), failResult(m), okResult(m, 1)} },
			true,
			false,
		},
		{
			"not_answering",
			func(m Model) []tea.Msg { return []tea.Msg{okResult(m, 1), failResult(m)} },
			false,
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withColor(t, termenv.ANSI, true)

			m := newModel(t, specs, Options{Scale: 10})
			m, _ = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			_, out := drive(t, m, c.results(m)...)

			row := lineWith(out, "203.0.113.7")
			if got := strings.Contains(row, sgrUnderline+"alpha\x1b[0m"); got != c.underline {
				t.Errorf("name underlined = %v, want %v\n---\n%q", got, c.underline, row)
			}

			if got := strings.HasPrefix(row, sgrBold); got != c.bold {
				t.Errorf("row bold = %v, want %v\n---\n%q", got, c.bold, row)
			}
		})
	}
}

// TestRecentFailureMarkWithoutColor checks NO_COLOR (the Ascii profile) drops the
// underline along with every other style, leaving the name as plain padded text.
func TestRecentFailureMarkWithoutColor(t *testing.T) {
	withColor(t, termenv.Ascii, true)

	specs := []config.TargetSpec{{Name: "alpha", Addr: "203.0.113.7", Relay: map[string]string{}}}
	m := newModel(t, specs, Options{Scale: 10})
	m, out := drive(t, m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		okResult(m, 1), failResult(m), okResult(m, 1),
	)

	if row := lineWith(out, "203.0.113.7"); strings.Contains(row, "\x1b") ||
		!strings.Contains(row, padRight("alpha", m.hostW)+" 203.0.113.7") {
		t.Errorf("without color the name should be plain padded text\n---\n%q", row)
	}
}

// TestRecentFailureMarkClearsWhenScrolledOut checks the mark follows the bar as drawn:
// once enough successes push the failure past the bar's width, the name is plain again,
// although the failure is still in the retained history.
func TestRecentFailureMarkClearsWhenScrolledOut(t *testing.T) {
	withColor(t, termenv.ANSI, true)

	specs := []config.TargetSpec{{Name: "alpha", Addr: "203.0.113.7", Relay: map[string]string{}}}
	m := newModel(t, specs, Options{Scale: 10})
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 40}, failResult(m))

	for range m.resW - 1 {
		m, _ = drive(t, m, okResult(m, 1))
	}

	if row := lineWith(m.View(), "203.0.113.7"); !strings.Contains(row, sgrUnderline) {
		t.Fatalf("failure at the bar's last cell should still mark the name\n---\n%q", row)
	}

	m, out := drive(t, m, okResult(m, 1))
	if row := lineWith(out, "203.0.113.7"); strings.Contains(row, sgrUnderline) {
		t.Errorf("failure scrolled out of the bar should clear the mark\n---\n%q", row)
	}

	if n := m.rows[0].Target.Len(); n <= m.resW {
		t.Errorf("history holds %d results, want more than the bar's %d", n, m.resW)
	}
}

// TestRecentFailureMarkMovesWithHiddenColumns checks the underline goes to the first
// shown identity column, so hiding HOSTNAME (then ADDRESS) moves it rather than
// dropping it, and hiding all three draws the row without it.
func TestRecentFailureMarkMovesWithHiddenColumns(t *testing.T) {
	specs := []config.TargetSpec{{Name: "alpha", Addr: "203.0.113.7", Relay: map[string]string{}}}

	cases := []struct {
		name   string
		hidden map[string]bool
		want   string // the underlined text, "" for none.
	}{
		{"host_shown", nil, "alpha"},
		{"host_hidden", map[string]bool{colHost: false}, "203.0.113.7"},
		{"host_addr_hidden", map[string]bool{colHost: false, colAddr: false}, "direct"},
		{"all_hidden", map[string]bool{colHost: false, colAddr: false, colVia: false}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withColor(t, termenv.ANSI, true)

			m := newModel(t, specs, Options{Scale: 10, Columns: c.hidden})
			m, out := drive(t, m,
				tea.WindowSizeMsg{Width: 120, Height: 40},
				okResult(m, 1), failResult(m), okResult(m, 1),
			)

			// The only target row is the last line; with every identity column hidden
			// there is no text of its own to find it by.
			row := out[strings.LastIndexByte(out, '\n')+1:]
			if c.want == "" {
				if strings.Contains(row, sgrUnderline) {
					t.Errorf("no identity column shown, want no underline\n---\n%q", row)
				}

				return
			}

			if !strings.Contains(row, sgrUnderline+c.want+"\x1b[0m") {
				t.Errorf("want %q underlined (via %q)\n---\n%q", c.want, m.rows[0].Target.Via, row)
			}

			if n := strings.Count(row, sgrUnderline); n != 1 {
				t.Errorf("underline count = %d, want 1\n---\n%q", n, row)
			}
		})
	}
}
