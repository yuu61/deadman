//go:build !linux

package prober

import "golang.org/x/net/icmp"

// Next-hop forcing is unavailable here; keep the portable listener buildable.
func filterEcho(_ *icmp.PacketConn, _ int) error { return nil }
