package configfile

import (
	"fmt"
	"strconv"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func (p *probeInput) echo() probe.EchoOptions {
	var echo probe.EchoOptions

	if raw := p.take("size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			p.err = fmt.Errorf("invalid size=%q: expected integer bytes", raw)
		}

		echo.Size = probe.PayloadBytes(n)
	}

	if raw := p.take("df"); raw != "" {
		on, err := parseBool(raw)
		if err != nil {
			p.err = fmt.Errorf("invalid df=%q: expected a boolean", raw)
		}

		echo.DF = on
	}

	return echo
}

func (p *probeInput) timeout() probe.Timeout {
	raw := p.take("timeout")
	if raw == "" {
		return probe.Timeout{}
	}

	// ParseDuration checks overflow and fractional seconds. Reject embedded units:
	// configuration writes seconds without a suffix.
	_, numberErr := strconv.ParseFloat(raw, 64)

	duration, durationErr := time.ParseDuration(raw + "s")
	if numberErr != nil || durationErr != nil {
		p.err = fmt.Errorf("invalid timeout=%q: expected seconds", raw)
	}

	return probe.TimeoutAfter(duration)
}
