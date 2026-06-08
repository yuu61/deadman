package ping

import (
	"bytes"
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

	return &hpingPinger{addr: s.Addr, port: port}, nil
}

func (p *hpingPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, hpingTimeout)
	defer cancel()

	// #nosec G204 -- addr/port come from the trusted local config file; running
	// hping3 against them is this prober's purpose.
	cmd := exec.CommandContext(ctx, "hping3", "-S", p.addr, "-p", p.port, "-c", "1")

	var out bytes.Buffer

	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return Result{Code: Failed, TTL: -1}
	}

	if m := reHping.FindStringSubmatch(out.String()); m != nil {
		rtt, _ := strconv.ParseFloat(m[1], 64)

		return Result{Success: true, Code: Success, RTT: rtt, TTL: -1}
	}

	return Result{Code: Failed, TTL: -1}
}
