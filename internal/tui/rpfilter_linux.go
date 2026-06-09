//go:build linux

package tui

import (
	"os"
	"strconv"
	"strings"
)

// rpFilterStrict reports whether Linux reverse-path filtering is in strict mode
// (value 1) on any interface. The kernel uses max(conf/all, conf/<iface>) per
// interface, so we flag strict when that maximum equals 1 for some interface.
// Loose (2) and off (0) do not drop the asymmetric replies that forced next-hop
// probes produce.
func rpFilterStrict() bool {
	const base = "/proc/sys/net/ipv4/conf"

	all := readRPFilter(base + "/all/rp_filter")

	entries, err := os.ReadDir(base)
	if err != nil {
		return all == 1
	}

	for _, e := range entries {
		// "all" is folded in via max; "default" is the template for future
		// interfaces (commonly 1) and not a live device, so it must be skipped to
		// avoid a persistent false positive.
		if e.Name() == "all" || e.Name() == "default" {
			continue
		}

		if maxInt(all, readRPFilter(base+"/"+e.Name()+"/rp_filter")) == 1 {
			return true
		}
	}

	return all == 1
}

// nexthopForcingSupported reports whether this platform can force a probe out a
// chosen next-hop (Linux AF_PACKET). See the non-Linux stub for other platforms.
func nexthopForcingSupported() bool { return true }

// readRPFilter reads a single integer rp_filter value, returning 0 when it cannot
// be read.
func readRPFilter(path string) int {
	b, err := os.ReadFile(path) // #nosec G304 -- fixed /proc path.
	if err != nil {
		return 0
	}

	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}

	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}

	return b
}
