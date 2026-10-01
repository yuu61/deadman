package monitoring

import (
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Event is a monitoring transition, independent of any UI framework. Only the session's
// Tasks make one, and only its Update reads one: a frontend carries events back without
// looking inside, so it can neither forge one nor bypass the generation check.
//
//sumtype:decl
type Event interface{ monitoringEvent() }

// roundStart begins a pass over the current targets.
type roundStart struct{ generation int }

// probeStart announces the target about to be probed in a sequential round, whose
// arrow moves to it before its probe runs.
type probeStart struct{ index, generation int }

// probeResult carries the outcome of one probe of the row at index. It holds no entity:
// a generation's rows never change (a reload starts a new generation), so Update matches
// the result to its target by generation and index alone, and only Update touches the
// target's statistics.
type probeResult struct {
	index, generation int
	result            probe.Result
}

// reloadDone carries a reread config, prepared off the state owner, back to it. id is
// the reload request it answers; only the latest request's result is applied. rows is
// nil when the config could not be read.
type reloadDone struct {
	id     int
	rows   *preparedRows
	lines  []Line
	loaded Loaded
}

func (roundStart) monitoringEvent()  {}
func (probeStart) monitoringEvent()  {}
func (probeResult) monitoringEvent() {}
func (reloadDone) monitoringEvent()  {}

// Task runs outside the state owner. It returns its event, or false when its session,
// generation or reload was canceled first and there is nothing to report.
type Task func() (Event, bool)
