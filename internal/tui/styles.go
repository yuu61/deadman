package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Layout constants ported from the original curses UI.
const (
	titleProgName = "Dead Man"
	titleVersion  = "[ver go-1.0.0]"

	arrow = " > "
	rear  = "   "

	maxHostnameLength = 20
	maxAddressLength  = 40

	refHeader = " LOSS  RTT  AVG  SNT"

	// pingInterval is the gap between consecutive targets in sync mode;
	// allTargetInterval is the pause between full ping rounds.
	pingInterval      = 50 * time.Millisecond
	allTargetInterval = 1 * time.Second
)

// spinnerChars is the wheel shown in the title bar in async mode.
const spinnerChars = `|/-\`

var (
	styleBold = lipgloss.NewStyle().Bold(true)
	styleUp   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	styleDown = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
)
