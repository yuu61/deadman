package monitoring

import (
	"context"
	"slices"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

const (
	probeInterval = 50 * time.Millisecond
	roundInterval = time.Second
	// Allow a canceled SNMP request and its independent cleanup deadline to finish.
	shutdownTimeout = 3 * time.Second
)

// sleep is the real clock's Wait.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// sessionPorts are what a session needs beyond its rows: rereading the config for a
// reload, logging each recorded result (log may be nil), and waiting between probes and
// rounds (nil waits in real time).
type sessionPorts struct {
	reread func(context.Context) (*preparedRows, []Line, Loaded)
	log    ResultLog
	wait   Wait
}

// Session owns the monitored rows across reloads: it schedules and cancels the probes,
// records their results, and applies reloads. Update, ResetStatistics, Reload and all
// state reads run on one owner goroutine; only Tasks and Close may run concurrently with
// it. A frontend executes Tasks and feeds their events back to Update.
type Session struct {
	parent       context.Context // the session's lifetime; generations and reloads live in it.
	stop         context.CancelFunc
	work         sessionWork
	ports        sessionPorts
	async        bool
	gen          *generation
	reloadID     int // the latest reload request; only its result is applied.
	answered     int // the latest reload request whose result was handled.
	reloadCancel context.CancelFunc
	logDropped   int // result lines the log has dropped over the session.
}

// generation is one target set and its probing state. A reload replaces the whole
// generation: canceling it stops its timers and probes and releases its adapters, and
// its events are recognized as stale by their Generation number.
type generation struct {
	ctx        context.Context // the generation's lifetime, spanning its tasks.
	cancel     context.CancelFunc
	id         int
	rows       []entry
	lines      []Line
	inflight   []bool
	pending    int
	roundStart time.Time
}

func newSession(
	ctx context.Context,
	ports sessionPorts,
	rows []entry,
	lines []Line,
	async bool,
) *Session {
	parent, stop := context.WithCancel(ctx)

	if ports.wait == nil {
		ports.wait = sleep
	}

	s := &Session{
		parent: parent, stop: stop, ports: ports, async: async,
		work: sessionWork{done: make(chan struct{})},
	}
	s.gen = newGeneration(parent, &s.work, 0, rows, lines)

	return s
}

// newGeneration creates generation id over rows. Its cancellation releases the rows'
// adapters; the rows are captured as they are, so that never reads live statistics.
func newGeneration(
	parent context.Context,
	work *sessionWork,
	id int,
	rows []entry,
	lines []Line,
) *generation {
	ctx, cancel := context.WithCancel(parent)
	work.release(ctx, rows)

	return &generation{
		ctx:      ctx,
		cancel:   cancel,
		id:       id,
		rows:     rows,
		lines:    lines,
		inflight: make([]bool, len(rows)),
	}
}

// Close stops monitoring: it cancels in-flight probes, timers and pending reloads and
// waits up to shutdownTimeout for running tasks and adapter releases, including retired
// generations. Tasks not yet started are skipped. It is safe to call repeatedly and
// from any goroutine.
func (s *Session) Close() { s.work.close(s.stop, shutdownTimeout) }

// Async reports whether the targets are probed in parallel rather than one by one.
func (s *Session) Async() bool { return s.async }

// Table returns one detached read model in config order, including static lines. Readers
// may change their copies without changing the session or its adapters.
func (s *Session) Table() []Line {
	lines := slices.Clone(s.gen.lines)
	for _, row := range s.gen.rows {
		lines[row.position] = Monitored{Target: row.target.Snapshot(), Plan: row.plan}
	}

	return lines
}

// ResetStatistics clears all histories. Like Update it changes the statistics, so call
// it on the state owner; read Table again afterwards.
func (s *Session) ResetStatistics() {
	for _, row := range s.gen.rows {
		row.target.Refresh()
	}
}

// Start schedules the first round of the current generation.
func (s *Session) Start() Task {
	return s.after(0, roundStart{generation: s.gen.id})
}

// Reload requests a reread of the config. Call it on the state owner and run the Task
// off it: the Task reads and builds the new rows while monitoring continues, and its
// event, fed to Update, applies them. When requests overlap only the latest one is
// applied; an earlier one finishing late, or any finishing after Close, is discarded
// and its adapters released.
func (s *Session) Reload() Task {
	if s.reloadCancel != nil {
		s.reloadCancel()
	}

	ctx, cancel := context.WithCancel(s.parent)
	s.reloadCancel = cancel
	s.reloadID++
	id, reread := s.reloadID, s.ports.reread

	return s.work.task(ctx, func() (Event, bool) {
		if ctx.Err() != nil {
			return nil, false
		}

		// The rows follow ctx: a reload canceled from here on releases them by itself.
		rows, lines, loaded := reread(ctx)
		s.work.prepared(rows)

		if ctx.Err() != nil {
			return nil, false
		}

		return reloadDone{id: id, rows: rows, lines: lines, loaded: loaded}, true
	})
}

// Update applies a monitoring event on the state-owning goroutine, including
// recording results and applying reloads. Stale, canceled and out-of-bounds events
// have no effects.
func (s *Session) Update(event Event, now time.Time) Transition {
	switch e := event.(type) {
	case reloadDone:
		return s.finishReload(e)
	case roundStart:
		return s.beginRound(e, now)
	case probeStart:
		return s.announce(e)
	case probeResult:
		return s.recordResult(e, now)
	default:
		return Transition{} // unreachable: Event is a closed set, each kind cased above.
	}
}

// beginRound starts a pass over the live generation's rows.
func (s *Session) beginRound(e roundStart, now time.Time) Transition {
	var out Transition
	if !s.current(e.generation) {
		return out
	}

	out.RoundStarted = true
	out.Tasks = s.startRound(now)
	out.Inflight = s.tableInflight()

	return out
}

// announce moves a sequential round on to its next row, whose probe then runs.
func (s *Session) announce(e probeStart) Transition {
	var out Transition
	if !s.current(e.generation) || !s.validIndex(e.index) {
		return out
	}

	index := s.gen.rows[e.index].position
	out.Probing = &index
	out.Tasks = []Task{s.probe(e.index)}

	return out
}

// current reports whether an event of generation belongs to the live generation.
func (s *Session) current(generation int) bool {
	return generation == s.gen.id && s.gen.ctx.Err() == nil
}

func (s *Session) tableInflight() []bool {
	states := make([]bool, len(s.gen.lines))
	for i, row := range s.gen.rows {
		states[row.position] = s.gen.inflight[i]
	}

	return states
}

// finishReload applies the latest reload's rows, or discards a superseded or late one.
// The live targets are read only here, on the owner, so the results that arrived while
// the reload was being prepared are carried over too.
func (s *Session) finishReload(done reloadDone) Transition {
	var out Transition

	if !s.work.begin(s.parent) {
		done.rows.discard()

		return out
	}
	defer s.work.wg.Done()

	if done.id != s.reloadID || done.id == s.answered || s.parent.Err() != nil {
		done.rows.discard()

		return out
	}

	s.answered = done.id
	out.Reload = &ReloadOutcome{Loaded: done.loaded}

	rows, ok := done.rows.take()
	if !ok {
		return out
	}

	carry(rows, s.gen.rows)
	s.gen.cancel()
	s.gen = newGeneration(s.parent, &s.work, s.gen.id+1, rows, done.lines)

	out.Reload.Applied = true
	out.Tasks = []Task{s.Start()}
	out.Inflight = s.tableInflight()

	return out
}

// recordResult folds a result of the live generation into its row. The row's probe of
// this round is then no longer in flight, which Recorded implies.
func (s *Session) recordResult(result probeResult, now time.Time) Transition {
	var out Transition
	if !s.current(result.generation) || !s.validIndex(result.index) {
		return out
	}

	reading, logged := s.record(s.gen.rows[result.index].target, result.result, now)

	out.Recorded = &Recorded{Index: s.gen.rows[result.index].position, Reading: reading}
	if status, ok := s.ports.log.(ResultLogStatus); ok {
		out.LogError = status.Err()
	}

	if !logged {
		s.logDropped++
		out.LogDropped = s.logDropped
	}

	if s.gen.inflight[result.index] {
		s.gen.inflight[result.index] = false
		s.gen.pending--
		out.Tasks, out.RoundCompleted = s.advance(result.index, now)
	}

	return out
}

// record folds a probe result into its target's statistics and logs it, reporting false
// when the log dropped the line. It runs on the goroutine that owns the target: the log
// receives a detached reading, so the ResultLog may write it from anywhere.
func (s *Session) record(
	t *monitor.Target,
	res probe.Result,
	now time.Time,
) (monitor.Reading, bool) {
	reading := t.Consume(res)

	if s.ports.log == nil {
		return reading, true
	}

	return reading, s.ports.log.Log(reading, now)
}

func (s *Session) validIndex(index int) bool {
	return index >= 0 && index < len(s.gen.rows)
}

// startRound begins a pass: a parallel one starts every probe at once, a sequential one
// announces its first target, whose probe runs once the arrow is on it.
func (s *Session) startRound(now time.Time) []Task {
	s.gen.roundStart = now
	s.gen.pending = 0
	clear(s.gen.inflight)

	if len(s.gen.rows) == 0 {
		return []Task{s.after(roundInterval, roundStart{generation: s.gen.id})}
	}

	if !s.async {
		return []Task{s.scheduleProbe(0, 0)}
	}

	tasks := make([]Task, 0, len(s.gen.rows))
	for i := range s.gen.rows {
		s.markInflight(i)
		tasks = append(tasks, s.probe(i))
	}

	return tasks
}

// scheduleProbe announces the sequential round's probe of index after delay.
func (s *Session) scheduleProbe(index int, delay time.Duration) Task {
	s.markInflight(index)

	return s.after(delay, probeStart{index: index, generation: s.gen.id})
}

func (s *Session) markInflight(index int) {
	s.gen.inflight[index] = true
	s.gen.pending++
}

func (s *Session) advance(index int, now time.Time) ([]Task, bool) {
	if next := index + 1; !s.async && next < len(s.gen.rows) {
		return []Task{s.scheduleProbe(next, probeInterval)}, false
	}

	if s.gen.pending > 0 {
		return nil, false
	}

	delay := roundInterval
	if s.async {
		delay = max(roundInterval-now.Sub(s.gen.roundStart), 0)
	}

	return []Task{s.after(delay, roundStart{generation: s.gen.id})}, true
}

func (s *Session) probe(index int) Task {
	gen, row := s.gen, s.gen.rows[index]

	return s.work.task(gen.ctx, func() (Event, bool) {
		if gen.ctx.Err() != nil {
			return nil, false
		}

		result := row.pinger.Send(gen.ctx)
		if gen.ctx.Err() != nil {
			return nil, false
		}

		return probeResult{index: index, generation: gen.id, result: result}, true
	})
}

// after returns a Task that emits event once delay has passed, unless the current
// generation is canceled first, while waiting or as the wait ends.
func (s *Session) after(delay time.Duration, event Event) Task {
	ctx, wait := s.gen.ctx, s.ports.wait

	return s.work.task(ctx, func() (Event, bool) {
		if !wait(ctx, delay) || ctx.Err() != nil {
			return nil, false
		}

		return event, true
	})
}
