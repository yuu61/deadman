package ping

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

const snmpTimeout = 5 * time.Second

// snmpPinger shells out to `snmpping` (SNMPv2, RFC4560). exec passes argv directly
// with no shell, so the community string needs no escaping.
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
	// relay (the snmp agent host) and addr (the ping target) are bare operands in the
	// snmpping argv, so a leading '-' would be parsed as a flag; reject both. community
	// is consumed as the argument to -c, so it is not an operand and is not checked.
	err := validateOperands(s.Relay["relay"], s.Addr)
	if err != nil {
		return nil, err
	}

	// A source= is silently ignored here (the relay originates the probe); the TUI
	// surfaces a startup warning via ping.SourceUnsupported rather than failing.
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

	out := &capWriter{limit: maxProbeOutput}
	cmd.Stdout = out

	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return failedResult
	}

	// snmpping (Net-SNMP apps/snmpping.c) prints the "rtt min/avg/max/stddev" summary
	// ONLY when pingResultsProbeResponses > 0, so a match here already implies a real
	// reply. This is unlike hping3, which prints its round-trip summary even at 100%
	// loss (see parseHpingResult), so gating snmp on the summary alone is correct and a
	// separate received-count check is not needed.
	if m := reSNMP.FindStringSubmatch(out.String()); m != nil {
		rtt, _ := strconv.ParseFloat(m[1], 64)

		return success(rtt, -1)
	}

	return failedResult
}
