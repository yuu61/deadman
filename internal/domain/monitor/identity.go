package monitor

import (
	"slices"
	"strconv"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// A row keeps its identity across reloads and restarts: Numbering gives the rows of a
// table their IDs, and Carry lets each live target continue in the fresh one with its ID.

// Numbering gives the rows of one table their IDs, in row order. A row's ID is the path it
// monitors (its probe.Plan.Identity) and its occurrence among the rows of that path, so
// the k-th row probing a path continues the k-th row that probed it before a reload (see
// Carry) and keeps writing the same log across restarts: duplicate rows keep separate
// histories and logs, and a display-name or credential edit keeps both. The zero value
// numbers a fresh table.
type Numbering struct{ seen map[string]int }

// Next builds the target of the table's next row, which probes by plan and shows name and
// addr.
func (n *Numbering) Next(plan probe.Plan, name, addr string) *Target {
	if n.seen == nil {
		n.seen = make(map[string]int)
	}

	path := plan.Identity()
	n.seen[path]++

	return NewTarget(path+"#"+strconv.Itoa(n.seen[path]), name, addr)
}

// Carry continues each live target in the fresh target with the same ID: the fresh
// target takes over its statistics and history and keeps its own name and address. An ID
// names one row, so each live target continues at most once, and duplicate rows keep
// separate histories. A row that could not be built has no target, so it neither
// continues a target nor is continued. Only the fresh targets change.
func Carry(fresh, live []*Target) {
	byID := make(map[string]*Target, len(live))

	for _, t := range live {
		byID[t.id] = t
	}

	for _, t := range fresh {
		if old, ok := byID[t.id]; ok {
			t.stats = old.stats
			t.history = slices.Clone(old.history)
			t.histNext, t.histLen, t.prevRTT = old.histNext, old.histLen, old.prevRTT

			delete(byID, t.id)
		}
	}
}
