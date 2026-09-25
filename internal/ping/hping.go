package ping

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const hpingTimeout = 5 * time.Second

// hpingPinger performs a TCP SYN probe via hping3 (Linux, requires root). The tcp
// option string has the form "dstport:80" (comma-separated key:value pairs).
type hpingPinger struct {
	addr string
	port string
}

func newHPingPinger(s Spec) (Pinger, error) {
	opts := map[string]string{}

	for opt := range strings.SplitSeq(s.TCP, ",") {
		if k, v, ok := strings.Cut(opt, ":"); ok {
			opts[k] = v
		}
	}

	port := opts["dstport"]
	if port == "" {
		return nil, fmt.Errorf("'dstport' is not specified in tcp option for %s", s.Addr)
	}

	// addr is a bare destination operand in the hping3 argv, so a leading '-' would be
	// parsed as a flag (argument injection); reject it.
	err := validateOperands(s.Addr)
	if err != nil {
		return nil, err
	}

	// A source= is silently ignored here (hping3 has no per-probe source); the TUI
	// surfaces a startup warning via ping.SourceUnsupported rather than failing.
	return &hpingPinger{addr: s.Addr, port: port}, nil
}

func (p *hpingPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, hpingTimeout)
	defer cancel()

	// #nosec G204 -- addr/port come from the trusted local config file; running
	// hping3 against them is this prober's purpose.
	cmd := exec.CommandContext(ctx, "hping3", "-S", p.addr, "-p", p.port, "-c", "1")

	out := &capWriter{limit: maxProbeOutput}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return failedResult
	}

	return parseHpingResult(out.String())
}

// parseHpingResult derives liveness from hping3's output. hping3 prints its
// "round-trip min/avg/max = ..." summary even on 100% loss (a down/filtered host), so
// a reply is recognized only when the received-packet count is > 0; the summary then
// carries the RTT. Gating on the summary alone would report every down tcp target as
// up — the exact case this mode exists to detect.
func parseHpingResult(out string) Result {
	m := reHpingRecv.FindStringSubmatch(out)
	if m == nil {
		return failedResult
	}

	if recv, _ := strconv.Atoi(m[1]); recv > 0 {
		if rm := reHping.FindStringSubmatch(out); rm != nil {
			rtt, _ := strconv.ParseFloat(rm[1], 64)

			return success(rtt, -1)
		}
	}

	return failedResult
}
