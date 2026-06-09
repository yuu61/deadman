//go:build !linux

package tui

// rpFilterStrict is Linux-specific; other platforms have no /proc rp_filter knob
// (and no next-hop forcing), so there is nothing to warn about.
func rpFilterStrict() bool { return false }
