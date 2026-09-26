package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/monitor"
	"github.com/yuu61/deadman/internal/palette"
)

// Layout constants for the TUI screen.
const (
	titleProgName = "Dead Man"

	// defaultVersion labels the build when no version was injected via -ldflags (e.g. a
	// `go install`/`go run` build). Release builds set Options.Version from the Makefile
	// (git describe), which titleLine renders instead.
	defaultVersion = "dev"

	arrow = " > "
	rear  = "   "

	maxHostnameLength = 20
	maxAddressLength  = 40
	maxViaLength      = 24

	// minResultWidth is the floor for the result-bar column when the terminal is
	// too narrow to give it the leftover space.
	minResultWidth = 10

	// colGutter separates adjacent newspaper columns in the multi-column ('['/']')
	// layout; colGutterWidth is its display width. The rule is an ASCII '|' (not the
	// box-drawing '│', which is East-Asian-Ambiguous and renders at width 2 under a
	// CJK terminal, skewing the grid); ASCII keeps the gutter exactly 3 columns by
	// every width ruler and terminal. The column-fit math (effectiveCols) and the
	// per-column content width both reference this single constant.
	colGutter      = " | "
	colGutterWidth = 3

	// pingInterval is the gap between consecutive targets in sync mode;
	// allTargetInterval is the pause between full ping rounds.
	pingInterval      = 50 * time.Millisecond
	allTargetInterval = 1 * time.Second
)

// spinnerChars is the wheel shown in the title bar in async mode.
const spinnerChars = `|/-\`

var (
	styleBold = lipgloss.NewStyle().Bold(true)
	styleDown = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red: warning lines.
	// styleFail draws failed probes (X/t/s) in magenta, apart from the ramp's danger red.
	styleFail = lipgloss.NewStyle().Foreground(palette.Failure())
)

// rttStyles holds the RESULT bar's per-level styles, colored safe → caution → danger
// by palette.Ramp. They are keyed by level count, so block and ascii, which share
// their thresholds, also share their colors ('b' between them recolors nothing).
var rttStyles = buildRTTStyles()

func buildRTTStyles() map[int][]lipgloss.Style {
	out := map[int][]lipgloss.Style{}

	for _, name := range monitor.BarNames() {
		bar, _ := monitor.ParseBar(name)

		n := bar.Levels()
		if _, ok := out[n]; ok {
			continue
		}

		styles := make([]lipgloss.Style, n)
		for i, c := range palette.Ramp(n) {
			styles[i] = lipgloss.NewStyle().Foreground(c)
		}

		out[n] = styles
	}

	return out
}

// rttStyle returns the style for a success at level (monitor.Level) on bar, clamping
// the level like Bar.GlyphAt so a stale index can never panic the render.
func rttStyle(bar monitor.Bar, level int) lipgloss.Style {
	styles := rttStyles[bar.Levels()]

	return styles[min(max(level, 0), len(styles)-1)]
}

// DetectBackground settles whether the terminal background is dark, which picks the RTT
// ramp's dark or light variant. Call it before the Bubble Tea program starts: lipgloss
// otherwise asks the terminal (OSC 11) at the first render, even with color off, and
// the reply would race Bubble Tea's key reader and could be read as keystrokes. lipgloss
// caches the answer, and a terminal that does not answer counts as dark. With no color
// to draw (NO_COLOR, or not a terminal) it records "dark" without asking.
func DetectBackground() {
	if lipgloss.ColorProfile() == termenv.Ascii {
		lipgloss.SetHasDarkBackground(true)

		return
	}

	lipgloss.HasDarkBackground()
}
