// Package monitor holds the monitored Target entity: the row identity it keeps across
// reloads (ID), and the running statistics folded from each probe result — send
// counters, loss, RTT/average/min/max, jitter, and the rolling result history the
// result bar is drawn from.
package monitor

import "github.com/yuu61/deadman/internal/domain/probe"

// State is the reachability state of a target.
type State int

// Reachability states of a target.
const (
	Unknown State = iota
	Up
	Down
)

// percentMultiplier scales a 0..1 ratio to a 0..100 percentage.
const percentMultiplier = 100.0

// jitterGain is the RFC 3550 §6.4.1 smoothing divisor: Jit += (|ΔRTT| - Jit)/16.
// The 1/16 EWMA self-decays, so JIT reflects recent variation on this unbounded
// stream rather than freezing into a lifetime statistic.
const jitterGain = 16.0

// historyCap bounds the retained result history. We keep a fixed-size ring and
// slice it to the current terminal width at render time, so history survives a
// terminal resize rather than being capped at insert time by a width-dependent
// length.
const historyCap = 256

// Stats are a target's running statistics, folded from each probe result.
type Stats struct {
	State    State
	Loss     int
	LossRate float64
	RTT      float64 // current.
	Min      float64 // min successful RTT (lifetime).
	Max      float64 // max successful RTT (lifetime).
	Jit      float64 // RFC 3550 smoothed jitter (EWMA of |ΔRTT|), ms.
	Tot      float64 // sum of all successful RTTs.
	Avg      float64 // mean RTT.
	Snt      int     // number sent.
}

// Target owns the identity, statistics and history of a monitored row. Mutations go
// through Consume and Refresh; Snapshot returns detached read data. How the row is
// probed is not the entity's concern: the Plan it was built from lives beside it.
type Target struct {
	id       string
	name     string
	addr     string
	stats    Stats          // running statistics.
	history  []probe.Result // ring of raw probe results; len == historyCap once seeded.
	histNext int            // ring index where the next result will be written.
	histLen  int            // number of valid results retained, capped at historyCap.
	prevRTT  float64        // the last successful RTT, for jitter.
}

// Snapshot is a detached, read-only view of a target: its identity, name, address,
// statistics and history at one moment. Changing its fields never changes the entity, and
// later changes of the entity never reach it; a reader follows the entity by Advance.
type Snapshot struct {
	Stats

	// ID identifies the row: a fresh target continues the live target with the same ID
	// across a reload (see Carry), and the row's log follows it across restarts.
	// Numbering makes it unique among the rows of a table.
	ID   string
	Name string
	Addr string // the displayed address; monitoring uses the compiled destination.

	results []probe.Result // oldest first.
}

// Reading is what one probe result left a target at: its identity, its statistics after
// the result, and the result. Consume returns it, so a reader holding the target's
// Snapshot follows it by Advance instead of copying the history again, and a log records
// it without the history.
type Reading struct {
	Stats

	ID     string // the row identity, as Snapshot.ID.
	Name   string
	Addr   string
	Result probe.Result
}

// NewTarget builds the monitored entity of one row. id identifies the row across reloads
// and restarts (see Snapshot.ID), and a table's rows take theirs from Numbering; name and
// addr are what the row shows.
func NewTarget(id, name, addr string) *Target {
	return &Target{id: id, name: name, addr: addr}
}

// Name returns the row's display name.
func (t *Target) Name() string { return t.name }

// Snapshot copies the identity, statistics and history for a reader.
func (t *Target) Snapshot() Snapshot {
	results := make([]probe.Result, 0, t.histLen)
	if t.histLen < historyCap {
		results = append(results, t.history[:t.histLen]...)
	} else {
		results = append(append(results, t.history[t.histNext:]...), t.history[:t.histNext]...)
	}

	return Snapshot{ID: t.id, Name: t.name, Addr: t.addr, Stats: t.stats, results: results}
}

// Len reports how many probe results are retained (at most historyCap).
func (t Snapshot) Len() int { return len(t.results) }

// At returns the i-th most recent probe result: At(0) is the newest, At(Len()-1) the
// oldest still retained. The TUI renders each to a glyph at view time, so the result
// bar re-buckets live when the RTT scale changes. Callers must keep 0 <= i < Len();
// the TUI bounds i with Len at render time.
func (t Snapshot) At(i int) probe.Result { return t.results[len(t.results)-1-i] }

// Advance returns the snapshot followed by r, the reading of the target's next result:
// r's statistics, and r's result as the newest, the oldest dropped past the history's
// bound. Like append, it may reuse the snapshot's storage, so only the returned snapshot
// is to be used afterwards.
func (t Snapshot) Advance(r Reading) Snapshot {
	t.Stats = r.Stats
	if len(t.results) >= historyCap {
		t.results = t.results[len(t.results)-historyCap+1:]
	}

	t.results = append(t.results, r.Result)

	return t
}

// Consume folds a probe result into the running statistics and returns the reading it
// leaves.
func (t *Target) Consume(res probe.Result) Reading {
	switch {
	case res.IsSuccess():
		t.stats.Snt++
		t.stats.State = Up
		t.stats.RTT = res.RTT
		t.stats.Tot += res.RTT
		t.stats.Avg = t.stats.Tot / float64(t.stats.Snt-t.stats.Loss)
		t.foldSuccessRTT(res.RTT)
	case res.IsObserved():
		t.stats.Snt++
		t.stats.Loss++
		t.stats.State = Down
		t.stats.RTT = 0
	default:
		// A relay/setup failure does not establish whether the target is up or down.
		t.stats.State = Unknown
		t.stats.RTT = 0
	}

	if t.stats.Snt > 0 {
		t.stats.LossRate = float64(t.stats.Loss) / float64(t.stats.Snt) * percentMultiplier
	}

	if t.history == nil {
		t.history = make([]probe.Result, historyCap)
	}

	t.history[t.histNext] = res
	t.histNext = (t.histNext + 1) % historyCap

	if t.histLen < historyCap {
		t.histLen++
	}

	return Reading{ID: t.id, Name: t.name, Addr: t.addr, Stats: t.stats, Result: res}
}

// Refresh resets all statistics and history (the 'r' key).
func (t *Target) Refresh() {
	t.stats = Stats{}
	t.history = nil
	t.histNext = 0
	t.histLen = 0
	t.prevRTT = 0
}

// foldSuccessRTT folds a successful probe's RTT into the running min/max and the
// RFC 3550 smoothed jitter. It assumes t.stats.Snt is already incremented, so Snt-Loss
// is the number of successes including this one; 1 means the first sample (seed
// min/max, no jitter delta yet). The success count is used rather than a
// prevRTT==0 sentinel so a genuine 0ms RTT is not mistaken for "no predecessor".
// Jitter is measured between consecutive successes; failures in between are
// skipped (matching mtr).
func (t *Target) foldSuccessRTT(rtt float64) {
	if t.stats.Snt-t.stats.Loss == 1 {
		t.stats.Min = rtt
		t.stats.Max = rtt
		t.prevRTT = rtt

		return
	}

	t.stats.Min = min(t.stats.Min, rtt)
	t.stats.Max = max(t.stats.Max, rtt)

	d := rtt - t.prevRTT
	if d < 0 {
		d = -d
	}

	t.stats.Jit += (d - t.stats.Jit) / jitterGain
	t.prevRTT = rtt
}
