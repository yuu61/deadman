//go:build unix

package tui

import (
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// InstallReloadSignal wires SIGHUP to a config reload (Unix only). The signal
// handler injects a reloadMsg into the running program.
func InstallReloadSignal(p *tea.Program) {
	c := make(chan os.Signal, 1)

	signal.Notify(c, syscall.SIGHUP)
	go func() {
		for range c {
			p.Send(reloadMsg{})
		}
	}()
}
