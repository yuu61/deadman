package monitoring

import (
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Line is one detached table line in config order. A monitored line includes its
// snapshot and plan, so the frontend never joins two independently indexed slices.
//
//sumtype:decl
type Line interface{ line() }

// Separator is a visual separator line with an optional label.
type Separator struct {
	Label string
}

// Monitored is a monitored row: a detached snapshot of its target, and the Plan it is
// probed by. Changing it never changes the session.
type Monitored struct {
	Target monitor.Snapshot
	Plan   probe.Plan
}

// Rejected is a row that could not be built. It is never probed, recorded or logged, so
// it has no statistics; Reason is why, as its build error says it.
type Rejected struct {
	Name, Addr, Reason string
}

func (Separator) line() {}
func (Monitored) line() {}
func (Rejected) line()  {}
