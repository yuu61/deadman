package prober

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const hpingTimeout = 5 * time.Second

var (
	// reHping captures the first min value of the summary line. The fractional part is
	// optional but matched when present, so a sub-millisecond reply (e.g. "= 0.4/...")
	// reports its real RTT instead of truncating to 0.
	reHping = regexp.MustCompile(`round-trip min/avg/max = (\d+(?:\.\d+)?)`)
	// reHpingRecv captures hping3's received-packet count ("N packets received").
	// hping3 prints the "round-trip min/avg/max = 0.0/0.0/0.0 ms" summary
	// UNCONDITIONALLY — even on 100% loss — so liveness must gate on received > 0,
	// not on the presence of that summary, or a down host reads as up. hping3
	// misspells its transmit word ("tramitted") but "packets received" is normal, so
	// matching on it is safe.
	reHpingRecv = regexp.MustCompile(`(\d+) packets received`)
	// ICMP errors also increment hping's receive count; only a TCP reply proves success.
	reHpingTCP = regexp.MustCompile(
		`(?m)^len=\d+ ip=\S+ .*sport=\d+ flags=\S+ .*rtt=\d+(?:\.\d+)? ms`,
	)
)

// hpingPinger performs a TCP SYN probe via hping3 (Linux, requires root) to the port of
// probe=tcp port=PORT.
type hpingPinger struct {
	addr string
	port string
}

func newHPingPinger(dest probe.Destination, t probe.TCP) (probe.Pinger, error) {
	// The destination is a bare operand in the hping3 argv, so a leading '-' would be
	// parsed as a flag (argument injection); reject it.
	addr := dest.String()

	err := validateOperands(addr)
	if err != nil {
		return nil, err
	}

	// TCP parameters contain only the destination port.
	return &hpingPinger{addr: addr, port: t.Port.String()}, nil
}

func (p *hpingPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, hpingTimeout)
	defer cancel()

	// hping3 reports on both streams; the parser reads them as one text.
	out, err := runCapped(
		ctx,
		[]string{"hping3", "-S", p.addr, "-p", p.port, "-c", "1"},
		nil,
	)
	if errors.Is(err, exec.ErrNotFound) {
		return probe.UnavailableResult()
	}

	return parseHpingResult(out.stdout + out.stderr)
}

// parseHpingResult derives liveness from hping3's output. hping3 prints its
// "round-trip min/avg/max = ..." summary even on 100% loss (a down/filtered host), so
// a reply requires both a positive received count and a TCP response line. hping also
// counts ICMP errors from intermediate routers as received packets; those do not
// establish TCP reachability. Only an observed TCP reply uses the summary RTT.
func parseHpingResult(out string) probe.Result {
	m := reHpingRecv.FindStringSubmatch(out)
	if m == nil {
		return probe.UnavailableResult()
	}

	recv, err := strconv.Atoi(m[1])
	if err != nil {
		return probe.UnavailableResult()
	}

	if recv <= 0 {
		return probe.FailedResult()
	}

	if !reHpingTCP.MatchString(out) {
		return probe.UnavailableResult()
	}

	rm := reHping.FindStringSubmatch(out)
	if rm == nil {
		return probe.UnavailableResult()
	}

	rtt, ok := parseRTT(rm[1])
	if !ok {
		return probe.UnavailableResult()
	}

	return probe.SuccessResult(rtt)
}
