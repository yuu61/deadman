//go:build unix

package tui

import (
	"context"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"
)

type terminalTestModel struct{}

func (terminalTestModel) Init() tea.Cmd                         { return tea.Quit }
func (m terminalTestModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (terminalTestModel) View() string                          { return "" }

func TestWatchTerminalStopsAfterProgramExit(t *testing.T) {
	for range 10 {
		ctx, cancel := context.WithCancel(t.Context())
		p := tea.NewProgram(
			terminalTestModel{},
			tea.WithContext(ctx),
			tea.WithInput(
				nil,
			),
			tea.WithOutput(io.Discard),
			tea.WithoutRenderer(),
			tea.WithoutSignalHandler(),
		)
		done := WatchTerminal(ctx, p)
		_, err := p.Run()

		cancel()

		if err != nil {
			t.Fatal(err)
		}

		timer := time.NewTimer(time.Second)
		select {
		case <-done:
		case <-timer.C:
			t.Fatal("terminal watcher did not release its resources")
		}

		timer.Stop()
	}
}

func TestWatchTerminalStopsBeforeProgramStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	p := tea.NewProgram(terminalTestModel{}, tea.WithContext(ctx))
	done := WatchTerminal(ctx, p)

	cancel()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
		t.Fatal("terminal watcher did not stop before Run")
	}
}

func TestTerminalHungUp(t *testing.T) {
	tests := []struct {
		name string
		errs map[int]error // per descriptor; absent answers the size.
		want bool
	}{
		{"live terminal", nil, false},
		{"redirected", map[int]error{unix.Stdin: unix.ENOTTY, unix.Stdout: unix.ENOTTY}, false},
		{"output redirected, input live", map[int]error{unix.Stdout: unix.ENOTTY}, false},
		{"hung up", map[int]error{unix.Stdin: unix.EIO, unix.Stdout: unix.EIO}, true},
		{"output hung up", map[int]error{unix.Stdout: unix.EIO}, true},
		{"revoked", map[int]error{unix.Stdin: unix.EBADF}, true},
		{"interrupted", map[int]error{unix.Stdout: unix.EINTR}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalHungUp(func(fd int) error { return tt.errs[fd] })
			if got != tt.want {
				t.Errorf("terminalHungUp = %v, want %v", got, tt.want)
			}
		})
	}
}
