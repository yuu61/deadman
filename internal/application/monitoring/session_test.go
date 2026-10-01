package monitoring

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

type pingerFunc func(context.Context) probe.Result

func (f pingerFunc) Send(ctx context.Context) probe.Result { return f(ctx) }

// mustEvent runs task and returns its event, failing the test when it reports none.
func mustEvent(t *testing.T, task Task) Event {
	t.Helper()

	event, ok := task()
	if !ok {
		t.Fatal("task reported no event")
	}

	return event
}

// runAsync runs task on its own goroutine, delivering whether it reported an event.
func runAsync(task Task) <-chan bool {
	reported := make(chan bool, 1)

	go func() {
		_, ok := task()
		reported <- ok
	}()

	return reported
}

// receiveReported waits for a task started by runAsync to finish.
func receiveReported(t *testing.T, reported <-chan bool) bool {
	t.Helper()

	select {
	case ok := <-reported:
		return ok
	case <-time.After(time.Second):
		t.Fatal("monitoring task did not finish")

		return false
	}
}

func testSession(t *testing.T, p probe.Pinger, async bool) (*Session, []entry) {
	t.Helper()

	svc := NewService(Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) { return p, nil },
		LoadConfig: source(
			config.Target{Name: "a", Addr: "192.0.2.1"},
			config.Separator{},
			config.Malformed{Name: "rejected", Addr: "192.0.2.3", Problem: "invalid config"},
			config.Target{Name: "b", Addr: "192.0.2.2"},
		).load,
		Host: capable,
	})
	session, _ := openSession(t, svc, async)

	return session, session.gen.rows
}

// The application drives both scheduling modes without a Bubble Tea program. A
// sequential round announces each target (so its arrow moves) before probing it; a
// parallel round starts every probe at once.
func TestSessionScheduling(t *testing.T) {
	for _, async := range []bool{false, true} {
		name := "sequential"
		if async {
			name = "parallel"
		}

		t.Run(name, func(t *testing.T) {
			s, rows := testSession(t, downPinger{}, async)
			if len(rows) != 2 || len(monitoredRows(s.Table())) != 2 {
				t.Fatal("visual separator entered the monitored target set")
			}

			now := time.Now()
			start := s.Update(mustEvent(t, s.Start()), now)

			want := 1
			if async {
				want = 2
			}

			if !start.RoundStarted || len(start.Tasks) != want {
				t.Fatalf("round start = %+v, want %d tasks", start, want)
			}

			// Which rows are being probed is a reported fact: a sequential round probes
			// the first target, a parallel one every target at once.
			wantInflight := []bool{true, false, false, async}
			if !slices.Equal(start.Inflight, wantInflight) {
				t.Fatalf("round start Inflight = %v, want %v", start.Inflight, wantInflight)
			}

			probeTask := start.Tasks[0]
			if !async {
				announced := s.Update(mustEvent(t, probeTask), now)
				if announced.Probing == nil || *announced.Probing != 0 {
					t.Fatalf("sequential probe announced %v, want row 0", announced.Probing)
				}

				probeTask = announced.Tasks[0]
			}

			result := mustEvent(t, probeTask)
			// Worker tasks must never mutate the live entity.
			if rows[0].target.Snapshot().Snt != 0 {
				t.Fatal("worker changed statistics")
			}

			completed := s.Update(result, now)
			if completed.Recorded == nil || completed.Recorded.Index != 0 || s.gen.inflight[0] {
				t.Fatalf("a reported probe is still in flight: %+v", completed)
			}

			if rows[0].target.Snapshot().Snt != 1 || rows[1].target.Snapshot().Snt != 0 {
				t.Fatal("result was not recorded on its own target")
			}

			if completed.RoundCompleted {
				t.Fatal("round reported complete before every probe reported")
			}

			completed = s.Update(mustEvent(t, secondProbe(t, s, start, completed, now)), now)
			if len(completed.Tasks) != 1 || rows[1].target.Snapshot().Snt != 1 ||
				completed.Recorded == nil || completed.Recorded.Index != 3 {
				t.Fatal("finished round did not schedule exactly one next round")
			}

			// Completion is a fact of both modes; whether it moves a spinner is the
			// frontend's call.
			if !completed.RoundCompleted {
				t.Fatal("the last result of the round did not report RoundCompleted")
			}
			// The waiting round timer must be canceled too.
			s.Close()

			if event, ok := completed.Tasks[0](); ok {
				t.Fatalf("canceled timer returned %T", event)
			}
		})
	}
}

// secondProbe is the probe of the round's second row, row 3 past the separator and the
// rejected line: a parallel round started it with the first, a sequential one announces
// it once the first has reported (completed).
func secondProbe(t *testing.T, s *Session, start, completed Transition, now time.Time) Task {
	t.Helper()

	if s.Async() {
		if len(completed.Tasks) != 0 {
			t.Fatal("parallel round advanced before all results")
		}

		return start.Tasks[1]
	}

	if len(completed.Tasks) != 1 {
		t.Fatal("next sequential probe not scheduled")
	}

	next := s.Update(mustEvent(t, completed.Tasks[0]), now)
	if next.Probing == nil || *next.Probing != 3 {
		t.Fatal("separator was not skipped")
	}

	return next.Tasks[0]
}

func TestSessionReloadCancelsProbeAndRejectsOldResults(t *testing.T) {
	started := make(chan struct{})
	p := pingerFunc(func(ctx context.Context) probe.Result {
		close(started)
		<-ctx.Done()

		return probe.SuccessResult(1)
	})
	s, rows := testSession(t, p, false)
	now := time.Now()
	round := s.Update(mustEvent(t, s.Start()), now)
	start := s.Update(mustEvent(t, round.Tasks[0]), now)

	done := runAsync(start.Tasks[0])

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}

	oldGen := s.gen.id
	if out := reload(s); out.Reload == nil || !out.Reload.Applied {
		t.Fatalf("reload outcome = %+v, want it applied", out.Reload)
	}

	if receiveReported(t, done) {
		t.Fatal("canceled probe reported its result")
	}

	old := probeResult{index: 0, generation: oldGen, result: probe.SuccessResult(1)}
	if out := s.Update(old, now); out.Recorded != nil || rows[0].target.Snapshot().Snt != 0 {
		t.Fatal("old generation result affected replacement")
	}

	s.Close()

	if out := s.Update(old, now); out.Recorded != nil {
		t.Fatal("closed session still recorded a result")
	}
}

func TestSessionParentCancellationStopsReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	svc := NewService(Ports{NewPinger: fakePingers(nil), LoadConfig: source().load, Host: capable})

	s, _, err := svc.Open(ctx, true)
	if err != nil {
		t.Fatal(err)
	}

	out := reload(s)

	cancel()

	if len(out.Tasks) != 1 {
		t.Fatalf("applied reload scheduled %d tasks, want the next round", len(out.Tasks))
	}

	if event, ok := out.Tasks[0](); ok {
		t.Fatalf("replacement survived parent cancellation: %T", event)
	}
}

// Only the latest of overlapping reload requests applies. An earlier one finishing
// late, a result delivered twice, and one finishing after Close are each discarded, and
// their adapters released exactly once.
func TestSessionAppliesOnlyTheLatestReload(t *testing.T) {
	var built []*closingPinger

	src := source()
	svc := NewService(Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			p := &closingPinger{closed: make(chan struct{}, 2)}
			built = append(built, p)

			return p, nil
		},
		LoadConfig: src.load,
		Host:       capable,
	})
	s, _ := openSession(t, svc, false)
	src.cfg.Lines = []config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}}

	late := mustEvent(t, s.Reload())
	latest := mustEvent(t, s.Reload())

	if out := s.Update(late, time.Now()); out.Reload != nil {
		t.Fatalf("a superseded reload was reported: %+v", out.Reload)
	}

	awaitClose(t, built[0])

	if out := s.Update(latest, time.Now()); out.Reload == nil || !out.Reload.Applied {
		t.Fatalf("the latest reload was not applied: %+v", out.Reload)
	}

	if out := s.Update(latest, time.Now()); out.Reload != nil || len(built[1].closed) != 0 {
		t.Fatal("a reload result delivered twice was applied or released again")
	}

	third := mustEvent(t, s.Reload())
	s.Close()
	awaitClose(t, built[1])

	if out := s.Update(third, time.Now()); out.Reload != nil {
		t.Fatalf("a reload finishing after Close was reported: %+v", out.Reload)
	}

	awaitClose(t, built[2])

	if event, ok := s.Reload()(); ok {
		t.Fatalf("a reload requested after Close still ran: %T", event)
	}
}

func TestSupersededReloadCancelsConfigRead(t *testing.T) {
	started := make(chan struct{})

	var reads atomic.Int32

	svc := NewService(Ports{
		NewPinger: fakePingers(nil),
		LoadConfig: func(ctx context.Context) (config.Config, error) {
			if reads.Add(1) == 2 {
				close(started)
				<-ctx.Done()

				return config.Config{}, ctx.Err()
			}

			return config.Config{}, nil
		},
		Host: capable,
	})
	s, _ := openSession(t, svc, false)
	finished := runAsync(s.Reload())

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first config read did not start")
	}

	second := s.Reload()

	if receiveReported(t, finished) {
		t.Fatal("canceled reload reported its result")
	}

	if out := s.Update(mustEvent(t, second), time.Now()); out.Reload == nil || !out.Reload.Applied {
		t.Fatal("replacement reload was not applied")
	}
}

// An event is matched to its target by generation and row: one of another generation, for
// a separator row or for a row that does not exist has no effect at all.
func TestSessionRejectsWrongTarget(t *testing.T) {
	s, _ := testSession(t, downPinger{}, true)
	gen := s.gen.id

	for _, event := range []Event{
		probeResult{index: 0, generation: gen + 1, result: probe.SuccessResult(1)},
		probeResult{index: 0, generation: gen - 1, result: probe.SuccessResult(1)},
		probeResult{index: 2, generation: gen, result: probe.SuccessResult(1)},
		probeResult{index: 99, generation: gen, result: probe.SuccessResult(1)},
		probeStart{index: -1, generation: gen},
		probeStart{index: 99, generation: gen},
		probeStart{index: 0, generation: gen - 1},
		roundStart{generation: gen - 1},
		roundStart{generation: gen + 1},
	} {
		out := s.Update(event, time.Now())
		if out.Recorded != nil || len(out.Tasks) != 0 || out.Probing != nil ||
			out.Inflight != nil || out.RoundStarted || out.RoundCompleted {
			t.Fatalf("invalid event %+v had effects: %+v", event, out)
		}
	}
}

// The waits between probes and rounds go through the Wait port, and a timer of a canceled
// generation emits nothing, whether it was canceled during the wait or as it ended.
func TestSessionWaitsThroughPort(t *testing.T) {
	var waited []time.Duration

	svc := NewService(Ports{
		NewPinger:  fakePingers(nil),
		LoadConfig: source(config.Target{Name: "a", Addr: "192.0.2.1"}).load,
		Host:       capable,
		// The wait reports the full delay even once the session has ended.
		Wait: func(_ context.Context, d time.Duration) bool {
			waited = append(waited, d)

			return true
		},
	})
	s, _ := openSession(t, svc, false)
	now := time.Now()

	round := s.Update(mustEvent(t, s.Start()), now)
	result := s.Update(mustEvent(t, s.Update(mustEvent(t, round.Tasks[0]), now).Tasks[0]), now)

	if !result.RoundCompleted || len(result.Tasks) != 1 {
		t.Fatalf("round did not complete: %+v", result)
	}

	next := result.Tasks[0]

	if !slices.Equal(waited, []time.Duration{0, 0}) {
		t.Fatalf("waited %v before the first round and probe, want no delay", waited)
	}

	if event, ok := next(); !ok || waited[2] != roundInterval {
		t.Fatalf("next round %T after %v, want it after %v", event, waited[2:], roundInterval)
	}

	// A wait that reports the full delay after its generation ended still emits nothing.
	late := s.Start()
	s.Close()

	if event, ok := late(); ok {
		t.Fatalf("a timer of a closed session emitted %T", event)
	}
}

// pacedSession opens a session over lines whose waits are recorded instead of slept.
func pacedSession(t *testing.T, async bool, lines ...config.Line) (*Session, *[]time.Duration) {
	t.Helper()

	waited := &[]time.Duration{}
	svc := NewService(Ports{
		NewPinger:  fakePingers(nil),
		LoadConfig: source(lines...).load,
		Host:       capable,
		Wait: func(_ context.Context, d time.Duration) bool {
			*waited = append(*waited, d)

			return true
		},
	})
	s, _ := openSession(t, svc, async)

	return s, waited
}

// A sequential round waits between its rows, so the arrow walks the table instead of
// the probes running back to back.
func TestSessionSpacesSequentialProbes(t *testing.T) {
	s, waited := pacedSession(
		t,
		false,
		config.Target{Name: "a", Addr: "192.0.2.1"},
		config.Target{Name: "b", Addr: "192.0.2.2"},
	)
	now := time.Now()

	round := s.Update(mustEvent(t, s.Start()), now)
	first := s.Update(mustEvent(t, round.Tasks[0]), now)

	recorded := s.Update(mustEvent(t, first.Tasks[0]), now)
	if len(recorded.Tasks) != 1 || recorded.RoundCompleted {
		t.Fatalf("first result did not schedule the second row: %+v", recorded)
	}

	mustEvent(t, recorded.Tasks[0])

	// The round and its first row start at once; the second row waits 50ms.
	if want := []time.Duration{0, 0, 50 * time.Millisecond}; !slices.Equal(*waited, want) {
		t.Fatalf("waited %v, want %v", *waited, want)
	}
}

// A parallel round starts once a second, counted from the start of the previous round:
// the time its probes took is subtracted, and a round slower than a second is followed
// at once rather than after another full second.
func TestSessionKeepsParallelRoundsOneSecondApart(t *testing.T) {
	for _, c := range []struct {
		name       string
		last, want time.Duration
	}{
		{"round shorter than a second", 400 * time.Millisecond, 600 * time.Millisecond},
		{"round longer than a second", 1500 * time.Millisecond, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, waited := pacedSession(
				t,
				true,
				config.Target{Name: "a", Addr: "192.0.2.1"},
				config.Target{Name: "b", Addr: "192.0.2.2"},
			)
			begin := time.Now()

			round := s.Update(mustEvent(t, s.Start()), begin)
			if len(round.Tasks) != 2 {
				t.Fatalf("parallel round started %d probes, want 2", len(round.Tasks))
			}

			s.Update(mustEvent(t, round.Tasks[0]), begin.Add(100*time.Millisecond))

			done := s.Update(mustEvent(t, round.Tasks[1]), begin.Add(c.last))
			if !done.RoundCompleted || len(done.Tasks) != 1 {
				t.Fatalf("last result did not complete the round: %+v", done)
			}

			mustEvent(t, done.Tasks[0])

			if want := []time.Duration{0, c.want}; !slices.Equal(*waited, want) {
				t.Fatalf("waited %v, want %v", *waited, want)
			}
		})
	}
}

// A table with nothing to probe (every line rejected) still waits a second between its
// rounds instead of spinning through empty ones.
func TestSessionWaitsBetweenEmptyRounds(t *testing.T) {
	for _, async := range []bool{false, true} {
		s, waited := pacedSession(
			t,
			async,
			config.Malformed{Name: "bad", Addr: "192.0.2.1", Problem: "invalid config"},
		)

		round := s.Update(mustEvent(t, s.Start()), time.Now())
		if len(round.Tasks) != 1 {
			t.Fatalf(
				"async=%v: empty round scheduled %d tasks, want the next round",
				async,
				len(round.Tasks),
			)
		}

		mustEvent(t, round.Tasks[0])

		if want := []time.Duration{0, time.Second}; !slices.Equal(*waited, want) {
			t.Fatalf("async=%v: waited %v, want %v", async, *waited, want)
		}
	}
}

type closingPinger struct{ closed chan struct{} }

func (*closingPinger) Send(context.Context) probe.Result { return probe.FailedResult() }
func (p *closingPinger) Close()                          { p.closed <- struct{}{} }

func TestSessionCancellationReleasesAdapterResources(t *testing.T) {
	p := &closingPinger{closed: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(t.Context())
	row := entry{target: monitor.NewTarget("h#1", "h", "192.0.2.1"), pinger: p}
	s := newSession(ctx, sessionPorts{}, []entry{row}, []Line{Monitored{}}, false)

	cancel()
	s.Close()
	s.Close()

	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("session did not release adapter resources")
	}

	select {
	case <-p.closed:
		t.Fatal("adapter was closed more than once")
	default:
	}
}

// A reload carries the live history over to the new rows, retires the old generation's
// adapters and keeps the new ones open for the next generation to probe with.
func TestSessionReloadCarriesHistoryAndReleasesOldAdapters(t *testing.T) {
	oldPinger := &closingPinger{closed: make(chan struct{}, 2)}
	newPinger := &closingPinger{closed: make(chan struct{}, 2)}
	current := oldPinger
	svc := NewService(Ports{
		NewPinger:  func(probe.Plan, string) (probe.Pinger, error) { return current, nil },
		LoadConfig: source(config.Target{Name: "h", Addr: "192.0.2.1"}).load,
		Host:       capable,
	})
	s, _ := openSession(t, svc, false)

	live := s.gen.rows[0].target
	live.Consume(probe.SuccessResult(5))

	current = newPinger

	reload(s)

	if got := s.gen.rows[0]; got.target == live || got.target.Snapshot().Snt != 1 ||
		got.pinger != newPinger {
		t.Fatalf("applied row = %+v, want fresh target with carried history and fresh adapter", got)
	}

	select {
	case <-oldPinger.closed:
	case <-time.After(time.Second):
		t.Fatal("the retired generation's adapter was not released")
	}

	select {
	case <-newPinger.closed:
		t.Fatal("the new generation's adapter was released while in use")
	default:
	}
}

func TestRowsAndTransitionsAreDetached(t *testing.T) {
	log := &recordingLog{}
	svc := NewService(Ports{
		NewPinger:  fakePingers(nil),
		LoadConfig: source(config.Target{Name: "h", Addr: "192.0.2.1"}).load,
		Host:       capable,
		Log:        log,
	})
	s, _ := openSession(t, svc, false)
	table := s.Table()

	monitored, ok := table[0].(Monitored)
	if !ok {
		t.Fatalf("table row = %T, want Monitored", table[0])
	}

	monitored.Target.Name = "changed"
	table[0] = Rejected{Name: "changed"}

	current, ok := s.Table()[0].(Monitored)
	if !ok || current.Target.Name != "h" {
		t.Fatal("table snapshot or layout shares session state")
	}

	rows := monitoredRows(s.Table())
	rows[0].Target.Name = "changed"
	rows[0].Target.Snt = 100
	rows[0].Plan = probe.Plan{}
	rows[0] = Monitored{}

	if plan := monitoredRows(s.Table())[0].Plan; plan.Destination().String() != "192.0.2.1" {
		t.Fatalf("a reader's plan write reached the session: %+v", plan)
	}

	out := s.Update(
		probeResult{index: 0, generation: s.gen.id, result: probe.SuccessResult(5)},
		time.Now(),
	)
	if out.Recorded == nil || out.Recorded.Index != 0 || out.Recorded.Reading.Name != "h" ||
		out.Recorded.Reading.Snt != 1 {
		t.Fatalf("row mutation escaped into the entity: %+v", out.Recorded)
	}

	out.Recorded.Reading.Snt = 200
	snapshot := monitoredRows(s.Table())[0].Target
	s.ResetStatistics()

	if snapshot.Snt != 1 || snapshot.Len() != 1 || monitoredRows(s.Table())[0].Target.Snt != 0 {
		t.Fatal("snapshot and entity share mutable state")
	}

	if len(log.lines) != 1 || log.lines[0].name != "h" || log.lines[0].snt != 1 {
		t.Fatalf("logging bypassed the entity: %+v", log.lines)
	}

	round := s.Update(mustEvent(t, s.Start()), time.Now())
	round.Inflight[0] = false

	if !slices.Equal(s.gen.inflight, []bool{true}) {
		t.Fatal("a transition's Inflight shares the session's state")
	}
}

// A result the log had to drop is reported with the session's running count of dropped
// lines, so the frontend can say the log lacks them; a logged result reports none.
func TestSessionReportsDroppedLogLines(t *testing.T) {
	log := &recordingLog{}
	svc := NewService(Ports{
		NewPinger:  fakePingers(nil),
		LoadConfig: source(config.Target{Name: "h", Addr: "192.0.2.1"}).load,
		Host:       capable,
		Log:        log,
	})
	s, _ := openSession(t, svc, true)

	result := func() Transition {
		return s.Update(
			probeResult{index: 0, generation: s.gen.id, result: probe.SuccessResult(5)},
			time.Now(),
		)
	}

	if out := result(); out.LogDropped != 0 {
		t.Fatalf("logged result reported %d dropped lines", out.LogDropped)
	}

	log.full = true

	for want := 1; want <= 2; want++ {
		if out := result(); out.Recorded == nil || out.LogDropped != want {
			t.Fatalf("dropped line %d reported LogDropped=%d (recorded=%v)",
				want, out.LogDropped, out.Recorded != nil)
		}
	}

	log.full = false

	if out := result(); out.LogDropped != 0 {
		t.Fatalf("logged result after drops reported %d", out.LogDropped)
	}
}
