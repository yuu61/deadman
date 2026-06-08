package ping

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

const snmpTimeout = 5 * time.Second

// snmpPinger shells out to `snmpping` (SNMPv2, RFC4560). With exec's argv passing
// there is no shell, so the community string needs no escaping (unlike the
// original which built a shell command line).
type snmpPinger struct {
	addr      string
	relay     string
	community string
}

func newSNMPPinger(s Spec) (Pinger, error) {
	if s.Relay["community"] == "" {
		return nil, fmt.Errorf("'community' is not specified for %s", s.Addr)
	}

	if s.Relay["relay"] == "" {
		return nil, fmt.Errorf("'relay' is not specified for %s", s.Addr)
	}

	return &snmpPinger{addr: s.Addr, relay: s.Relay["relay"], community: s.Relay["community"]}, nil
}

func (p *snmpPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, snmpTimeout)
	defer cancel()

	// #nosec G204 -- relay host and community come from the trusted local config
	// file; shelling out to snmpping with them is this relay mode's purpose.
	cmd := exec.CommandContext(
		ctx,
		"snmpping",
		"-Cc1",
		"-v",
		"2c",
		"-c",
		p.community,
		p.relay,
		p.addr,
	)

	var out bytes.Buffer

	cmd.Stdout = &out

	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return Result{Code: Failed, TTL: -1}
	}

	if m := reSNMP.FindStringSubmatch(out.String()); m != nil {
		rtt, _ := strconv.ParseFloat(m[1], 64)

		return Result{Success: true, Code: Success, RTT: rtt, TTL: -1}
	}

	return Result{Code: Failed, TTL: -1}
}
