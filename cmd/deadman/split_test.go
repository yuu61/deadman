package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/presentation/tui"
)

// Exercise the config parser, CLI precedence and view keys together. A column
// guide must never force or cap the requested number of display columns.
func TestCommandSplitGuidesDoNotLimitColumnCount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured int
		flags      []string
		want       int
	}{
		{"configured one", 1, nil, 1},
		{"configured two", 2, nil, 2},
		{"configured three with two guides", 3, nil, 3},
		{"configured four with two guides", 4, nil, 4},
		{"configured seven with two guides", 7, nil, 7},
		{"CLI one overrides three", 3, []string{"--split", "1"}, 1},
		{"CLI five overrides one", 1, []string{"-c", "5"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := fmt.Sprintf(
				"split %d\n#pane LAN\n%s#pane WAN\n%s",
				tc.configured,
				strings.Repeat("#blank\n", 12),
				strings.Repeat("#blank\n", 6),
			)
			path := writeConfig(t, input)
			args := append([]string{path}, tc.flags...)

			code := execute(args, newService, func(m tui.Model) error {
				defer m.Close()

				m = updateDisplay(t, m, tea.WindowSizeMsg{Width: 1600, Height: 30})
				assertCommandColumns(t, m, tc.want)

				for remaining := tc.want; remaining > 1; remaining-- {
					m = updateDisplay(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
					assertCommandColumns(t, m, remaining-1)
				}

				for count := 2; count <= 8; count++ {
					m = updateDisplay(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
					assertCommandColumns(t, m, count)
				}

				return nil
			})
			if code != 0 {
				t.Fatalf("command exited with %d", code)
			}
		})
	}
}

func updateDisplay(t *testing.T, m tui.Model, message tea.Msg) tui.Model {
	t.Helper()

	next, cmd := m.Update(message)
	model, ok := next.(tui.Model)

	if !ok || cmd != nil {
		t.Fatalf("display update returned %T, command=%v", next, cmd != nil)
	}

	return model
}

func assertCommandColumns(t *testing.T, m tui.Model, want int) {
	t.Helper()

	view := m.View()
	if got := strings.Count(view, "HOSTNAME"); got != want {
		t.Fatalf("display has %d columns, want %d: %q", got, want, view)
	}

	for _, label := range []string{"LAN", "WAN"} {
		if !strings.Contains(view, label) {
			t.Errorf("description %q disappeared after changing split: %q", label, view)
		}
	}
}
