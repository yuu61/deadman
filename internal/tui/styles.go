package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Layout constants for the TUI screen.
const (
	titleProgName = "Dead Man"
	titleVersion  = "[ver 1.0.0]"

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
	styleUp   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green.
	styleDown = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red.
)
