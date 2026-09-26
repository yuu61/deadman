package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/ping"
)

// withColor makes lipgloss's default renderer emit color at profile p on a dark or light
// background for the rest of the test. Tests otherwise render with no color at all
// (stdout is not a terminal), which would hide the ramp.
func withColor(t *testing.T, p termenv.Profile, dark bool) {
	t.Helper()

	prevProfile, prevDark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()

	lipgloss.SetColorProfile(p)
	lipgloss.SetHasDarkBackground(dark)

	t.Cleanup(func() {
		lipgloss.SetColorProfile(prevProfile)
		lipgloss.SetHasDarkBackground(prevDark)
	})
}

// TestResultBarColorsByLevel feeds a fast, a middling and an overflowing RTT plus a
// failure, and checks each cell's color at every depth: the ramp runs from the safe
// green through the caution yellow to the danger red, the light background gets the
// darkened variant, 16 colors fall back to the ANSI green/yellow/red (bright on a dark
// background), and a failure is the terminal's magenta (bright on a dark background),
// never the danger red. At scale 10 the RTTs 0.5 / 35 / 5000 are levels 0 / 3 / 7.
//
// The expected escapes come from termenv itself: it quantizes a hex color on its own
// (#03AF7A is sent as 3;175;121), and what matters here is which palette color each
// cell asks for.
func TestResultBarColorsByLevel(t *testing.T) {
	cases := []struct {
		name    string
		profile termenv.Profile
		dark    bool
		colors  [3]string // levels 0, 3 and 7 (palette.Ramp(8)).
		fail    string
	}{
		{
			"truecolor_dark",
			termenv.TrueColor,
			true,
			[3]string{"#03AF7A", "#E5E838", "#FF4B00"},
			"13",
		},
		{
			"truecolor_light",
			termenv.TrueColor,
			false,
			[3]string{"#02A976", "#989A21", "#FF4B00"},
			"5",
		},
		{"256_dark", termenv.ANSI256, true, [3]string{"36", "184", "202"}, "13"},
		{"256_light", termenv.ANSI256, false, [3]string{"65", "136", "202"}, "5"},
		{"ansi_dark", termenv.ANSI, true, [3]string{"10", "11", "9"}, "13"},
		{"ansi_light", termenv.ANSI, false, [3]string{"2", "3", "1"}, "5"},
	}

	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withColor(t, c.profile, c.dark)

			m := newModel(t, specs, Options{Scale: 10})

			fail := okResult(m, 0)
			fail.res = ping.Result{Code: ping.Failed}

			_, out := drive(t, m,
				tea.WindowSizeMsg{Width: 200, Height: 40},
				okResult(m, 0.5), okResult(m, 35), okResult(m, 5000), fail,
			)

			sgr := func(color string) string {
				return "\x1b[" + c.profile.Color(color).Sequence(false) + "m"
			}

			want := []string{
				sgr(c.colors[0]) + "▁",
				sgr(c.colors[1]) + "▄",
				sgr(c.colors[2]) + "█",
				sgr(c.fail) + "X",
			}

			row := lineWith(out, "1.2.3.4")
			for _, w := range want {
				if !strings.Contains(row, w) {
					t.Errorf("row lacks %q\n---\n%q", w, row)
				}
			}
		})
	}
}

// TestDetectBackgroundWithoutColor checks the no-color path settles the background
// without querying: under go test stdout is not a terminal (the Ascii profile), so the
// call must return at once and leave lipgloss on the dark default.
func TestDetectBackgroundWithoutColor(t *testing.T) {
	withColor(t, termenv.Ascii, false)

	DetectBackground()

	if !lipgloss.HasDarkBackground() {
		t.Error("DetectBackground under the Ascii profile left a light background, want dark")
	}
}
