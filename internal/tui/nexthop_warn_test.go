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
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // ipv6Ignored.
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

	if want := []string{"v6"}; !slices.Equal(c.ipv6Ignored, want) {
		t.Errorf("ipv6Ignored = %v, want %v", c.ipv6Ignored, want)
	}

	if want := []string{"v4", "byname"}; !slices.Equal(c.forced, want) {
		t.Errorf("forced = %v, want %v", c.forced, want)
	}
}
