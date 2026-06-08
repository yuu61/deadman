//go:build !unix

package tui

import tea "github.com/charmbracelet/bubbletea"

// InstallReloadSignal is a no-op on platforms without SIGHUP (e.g. Windows),
// where the 'R' key triggers a reload instead.
func InstallReloadSignal(p *tea.Program) {}
