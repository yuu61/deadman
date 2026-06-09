package tui

import (
	"slices"
	"testing"

	"github.com/yuu61/deadman/internal/config"
)

func TestClassifyNexthop(t *testing.T) {
	specs := []config.TargetSpec{
		{
			Name: "plain",
			Addr: "8.8.8.8",
		}, // no nexthop: skipped.
		{
			Name:  "v4",
			Addr:  "8.8.8.8",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // forced.
		{
			Name:  "byname",
			Addr:  "example.com",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // forced (unresolved name).
		{
			Name:  "v6",
			Addr:  "2001:db8::1",
			Relay: map[string]string{"nexthop": "2001:db8::ffff"},
		}, // forced (IPv6 gateway matches IPv6 target).
		{
			Name:  "mismatch",
			Addr:  "2001:db8::1",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // familyMismatch (IPv6 target, IPv4 gateway).
		{
			Name:  "ssh",
			Addr:  "8.8.8.8",
			Relay: map[string]string{"nexthop": "192.0.2.1", "relay": "h"},
		}, // modeIgnored.
		{
			Name:  "tcp",
			Addr:  "8.8.8.8",
			TCP:   "dstport:80",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // modeIgnored.
	}

	c := classifyNexthop(specs)

	if want := []string{"ssh", "tcp"}; !slices.Equal(c.modeIgnored, want) {
		t.Errorf("modeIgnored = %v, want %v", c.modeIgnored, want)
	}

	if want := []string{"mismatch"}; !slices.Equal(c.familyMismatch, want) {
		t.Errorf("familyMismatch = %v, want %v", c.familyMismatch, want)
	}

	if want := []string{"v4", "byname", "v6"}; !slices.Equal(c.forced, want) {
		t.Errorf("forced = %v, want %v", c.forced, want)
	}

	if !c.forcedV4 {
		t.Error("forcedV4 = false, want true (IPv4 and name targets are present)")
	}
}
