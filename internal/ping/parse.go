package ping

import (
	"regexp"
	"strconv"
)

// msPerS converts seconds to milliseconds.
const msPerS = 1000.0

var (
	reTimeFloat = regexp.MustCompile(`time=(\d+\.\d+)`)
	reTimeInt   = regexp.MustCompile(`time=(\d+)`)
	reTTL       = regexp.MustCompile(`ttl=(\d+)`)
	reHlim      = regexp.MustCompile(`hlim=(\d+)`)
	// reSNMP/reHping capture the first min value of the summary line. The fractional
	// part is optional but matched when present, so a sub-millisecond reply
	// (e.g. "= 0.4/...") reports its real RTT instead of truncating to 0.
	reSNMP  = regexp.MustCompile(`rtt min/avg/max/stddev = (\d+(?:\.\d+)?)`)
	reHping = regexp.MustCompile(`round-trip min/avg/max = (\d+(?:\.\d+)?)`)
	// reHpingRecv captures hping3's received-packet count ("N packets received").
	// hping3 prints the "round-trip min/avg/max = 0.0/0.0/0.0 ms" summary
	// UNCONDITIONALLY — even on 100% loss — so liveness must gate on received > 0,
	// not on the presence of that summary, or a down host reads as up. hping3
	// misspells its transmit word ("tramitted") but "packets received" is normal, so
	// matching on it is safe.
	reHpingRecv = regexp.MustCompile(`(\d+) packets received`)
	// reRouterOSUnit matches one "<n><unit>" run of a RouterOS duration. Each unit is
	// independently optional and summed, so any combination ("1ms500us", "500us",
	// "5ms", "2s") parses — unlike a fixed ms+us shape that dropped whole-ms values.
	reRouterOSUnit = regexp.MustCompile(`(\d+)(us|ms|s)`)
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

// ParseRouterOSMinRTT parses a RouterOS min-rtt duration into milliseconds. Each
// unit run (s/ms/us) is summed, so "1ms500us" -> 1.5, "500us" -> 0.5, "5ms" -> 5
// and "2s" -> 2000 all parse. ok is false when no recognizable unit is present.
func ParseRouterOSMinRTT(s string) (float64, bool) {
	matches := reRouterOSUnit.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return 0, false
	}

	var ms float64

	for _, m := range matches {
		n, _ := strconv.ParseFloat(m[1], 64)

		switch m[2] {
		case "s":
			ms += n * msPerS
		case "ms":
			ms += n
		case "us":
			ms += n / usPerMs
		default:
			// Unreachable: reRouterOSUnit only captures s/ms/us.
		}
	}

	return ms, true
}
