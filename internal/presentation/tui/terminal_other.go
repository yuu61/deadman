//go:build !unix

package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// WatchTerminal is a no-op on platforms without SIGHUP (e.g. Windows), where the 'R'
// key triggers a reload instead.
func WatchTerminal(_ context.Context, _ *tea.Program) <-chan error {
	done := make(chan error)
	close(done)

	return done
}
