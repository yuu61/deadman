package ping

import (
	"regexp"
	"strconv"
)

var (
	reTimeFloat = regexp.MustCompile(`time=(\d+\.\d+)`)
	reTimeInt   = regexp.MustCompile(`time=(\d+)`)
	reTTL       = regexp.MustCompile(`ttl=(\d+)`)
	reHlim      = regexp.MustCompile(`hlim=(\d+)`)
	reSNMP      = regexp.MustCompile(`rtt min/avg/max/stddev = (\d+)`)
	reHping     = regexp.MustCompile(`round-trip min/avg/max = (\d+)`)
	reRouterOS  = regexp.MustCompile(`((\d+)ms)?(\d+)us`)
)

// ParsePingOutput extracts a Result from the stdout of a `ping`/`ping6` run,
// matching the time=<float|int> and ttl=/hlim= fields.
func ParsePingOutput(out string) Result {
	var rtt float64

	matched := false

	if m := reTimeFloat.FindStringSubmatch(out); m != nil {
		rtt, _ = strconv.ParseFloat(m[1], 64)
		matched = true
	} else if m := reTimeInt.FindStringSubmatch(out); m != nil {
		rtt, _ = strconv.ParseFloat(m[1], 64)
		matched = true
	}

	if !matched {
		return Result{Code: Failed, TTL: -1}
	}

	ttl := -1
	if m := reTTL.FindStringSubmatch(out); m != nil {
		ttl, _ = strconv.Atoi(m[1])
	} else if m := reHlim.FindStringSubmatch(out); m != nil {
		ttl, _ = strconv.Atoi(m[1])
	}

	return Result{Success: true, Code: Success, RTT: rtt, TTL: ttl}
}

// ParseRouterOSMinRTT parses RouterOS min-rtt values like "1ms500us" into
// milliseconds (1.5). The "ms" group is optional, e.g. "500us" -> 0.5.
func ParseRouterOSMinRTT(s string) (float64, bool) {
	m := reRouterOS.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}

	var ms, us float64
	if m[2] != "" {
		ms, _ = strconv.ParseFloat(m[2], 64)
	}

	if m[3] != "" {
		us, _ = strconv.ParseFloat(m[3], 64)
	}

	return ms + us/usPerMs, true
}
