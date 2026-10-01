//go:build linux

package prober

import (
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// rpFilterStrict reports whether Linux reverse-path filtering is in strict mode
// (value 1) on any interface of this host. The kernel's per-interface IPv4 settings,
// /proc/sys/net/ipv4/conf, are a directory per interface (plus "all" and "default"),
// each with an rp_filter file.
func rpFilterStrict() bool {
	return strictRPFilter(os.DirFS("/proc/sys/net/ipv4/conf"))
}

// strictRPFilter reports whether reverse-path filtering in conf is strict (value 1) on
// any interface. The kernel uses max(conf/all, conf/<iface>) per interface, so we flag
// strict when that maximum equals 1 for some interface. Loose (2) and off (0) do not
// drop the asymmetric replies that forced next-hop probes produce.
func strictRPFilter(conf fs.FS) bool {
	all := readRPFilter(conf, "all")

	entries, err := fs.ReadDir(conf, ".")
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

		if max(all, readRPFilter(conf, e.Name())) == 1 {
			return true
		}
	}

	return all == 1
}

// readRPFilter reads the integer rp_filter value of iface in conf, returning 0 when it
// cannot be read.
func readRPFilter(conf fs.FS, iface string) int {
	b, err := fs.ReadFile(conf, iface+"/rp_filter")
	if err != nil {
		return 0
	}

	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}

	return v
}
