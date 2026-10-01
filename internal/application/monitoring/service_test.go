package monitoring

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// errUnbuildable is what fakePingers returns for a target it is told cannot be built,
// standing in for e.g. a missing required relay attribute.
var errUnbuildable = errors.New("unbuildable target")

// downPinger never gets a reply, like a target that is down.
type downPinger struct{}

func (downPinger) Send(context.Context) probe.Result { return probe.FailedResult() }

// fakePingers is a PingerFactory that builds a never-replying Pinger for every plan
// except those whose address is in *bad, for which it fails like an unbuildable target.
// bad is a pointer so a test can fix a target between reads, like editing the file.
func fakePingers(bad *[]string) PingerFactory {
	return func(plan probe.Plan, _ string) (probe.Pinger, error) {
		if bad != nil && slices.Contains(*bad, plan.Destination().String()) {
			return nil, errUnbuildable
		}

		return downPinger{}, nil
	}
}

// fakeSource is a ConfigSource over one in-memory config that a test may change between
// reads, like editing the config file; a non-nil err makes the reads fail.
type fakeSource struct {
	cfg config.Config
	err error
}

func (f *fakeSource) load(context.Context) (config.Config, error) {
	if f.err != nil {
		return config.Config{}, f.err
	}

	return f.cfg, nil
}

// configOf is a config listing lines.
func configOf(lines ...config.Line) config.Config { return config.Config{Lines: lines} }

// source returns a fakeSource whose config lists lines.
func source(lines ...config.Line) *fakeSource {
	return &fakeSource{cfg: configOf(lines...)}
}

// openSession opens a session on svc and closes it when the test ends.
func openSession(t *testing.T, svc *Service, async bool) (*Session, Loaded) {
	t.Helper()

	s, loaded, err := svc.Open(t.Context(), async)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(s.Close)

	return s, loaded
}

// reload runs one reload request to completion on s and returns its transition.
func reload(s *Session) Transition {
	event, ok := s.Reload()()
	if !ok {
		return Transition{}
	}

	return s.Update(event, time.Now())
}

// logLine is one ResultLog.Log call as recordingLog saw it.
type logLine struct {
	id    string
	name  string
	state monitor.State
	rtt   float64
	avg   float64
	snt   int
	now   time.Time
}

// recordingLog is a ResultLog that keeps every line it is given, or drops each while full.
type recordingLog struct {
	lines []logLine
	full  bool
}

func (l *recordingLog) Log(target monitor.Reading, now time.Time) bool {
	if l.full {
		return false
	}

	l.lines = append(
		l.lines,
		logLine{
			id:    target.ID,
			name:  target.Name,
			state: target.State,
			rtt:   target.RTT,
			avg:   target.Avg,
			snt:   target.Snt,
			now:   now,
		},
	)

	return true
}

// Open reads the config through the port and hands its display directives and every
// warning to the frontend; a read error starts no session at all.
func TestOpen(t *testing.T) {
	src := source(config.Target{Name: "h", Addr: "192.0.2.1"})
	src.cfg.Notes = []config.Note{{Name: "h", Addr: "192.0.2.1", UnterminatedQuote: true}}
	src.cfg.Display = config.Display{Columns: map[string]bool{"MIN": false}, Glyph: "ascii"}
	svc := NewService(Ports{NewPinger: fakePingers(nil), LoadConfig: src.load, Host: capable})

	s, loaded := openSession(t, svc, false)
	if rows := monitoredRows(s.Table()); len(rows) != 1 || rows[0].Target.Name != "h" {
		t.Fatalf("rows = %+v, want the configured target", rows)
	}

	if loaded.Display.Glyph != "ascii" || loaded.Display.Columns["MIN"] {
		t.Errorf("display = %+v, want the config's directives", loaded.Display)
	}

	if len(loaded.Warnings) != 1 || loaded.Warnings[0] != (UnterminatedQuote{Name: "h"}) {
		t.Errorf("warnings = %+v, want the parse problem", loaded.Warnings)
	}

	src.err = errors.New("open deadman.conf: no such file or directory")

	failed, _, err := svc.Open(t.Context(), false)
	if err == nil || failed != nil {
		t.Fatalf(
			"Open with an unreadable config = %v, %v; want no session and the error",
			failed,
			err,
		)
	}
}

// A reload that cannot read its config keeps the current rows, but the reason must
// surface as a warning rather than the reload silently doing nothing.
func TestReloadFailureSurfacesWarning(t *testing.T) {
	src := source(config.Target{Name: "h", Addr: "192.0.2.1"})
	svc := NewService(Ports{NewPinger: fakePingers(nil), LoadConfig: src.load, Host: capable})
	s, _ := openSession(t, svc, false)
	before := s.gen

	src.err = errors.New("open does-not-exist.conf: no such file or directory")
	out := reload(s)

	if out.Reload == nil || out.Reload.Applied {
		t.Fatalf("reload outcome = %+v, want a failed, unapplied reload", out.Reload)
	}

	w := out.Reload.Warnings
	if failed, ok := onlyWarning[ReloadFailed](w); !ok ||
		!strings.Contains(failed.Reason, "does-not-exist.conf") {
		t.Errorf("warnings = %v, want a single 'reload failed' entry naming the file", w)
	}

	if s.gen != before || len(out.Tasks) != 0 {
		t.Error("a failed reload replaced or restarted the monitored rows")
	}
}

// A target whose Pinger cannot be built must not abort the whole program: construction
// succeeds, the bad target becomes a rejected line that always shows a failure, and a
// warning names it — so monitoring of the valid targets continues.
func TestBadTargetIsStaticRejectedRow(t *testing.T) {
	svc := NewService(Ports{NewPinger: fakePingers(&[]string{"1.1.1.1"}), Host: capable})

	rows, lines, loaded := svc.build(configOf(
		config.Target{Name: "good", Addr: "8.8.8.8"},
		config.Target{Name: "bad", Addr: "1.1.1.1"},
	))

	want := []Line{
		Monitored{},
		Rejected{Name: "bad", Addr: "1.1.1.1", Reason: errUnbuildable.Error()},
	}
	if len(rows) != 1 || !reflect.DeepEqual(lines, want) {
		t.Fatalf("rows=%+v lines=%+v, want lines %+v", rows, lines, want)
	}

	if failed, ok := onlyWarning[TargetBuildFailed](loaded.Warnings); !ok ||
		failed.Reason != errUnbuildable.Error() {
		t.Fatalf("warnings=%+v", loaded.Warnings)
	}
}

// A line whose attributes describe no probe (here an unknown probe=) is rejected with the
// parser's reason and never reaches the adapter factory.
func TestMalformedLineDoesNotReachPingerFactory(t *testing.T) {
	built := 0
	svc := NewService(Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			built++

			return downPinger{}, nil
		},
		Host: capable,
	})

	rows, lines, loaded := svc.build(configOf(config.Malformed{
		Name: "typo", Addr: "192.0.2.1", Problem: `unknown probe="pign"`,
	}))

	want := TargetBuildFailed{
		Name:   "typo",
		Addr:   "192.0.2.1",
		Reason: `invalid configuration: unknown probe="pign"`,
	}
	if built != 0 || len(rows) != 0 || len(rejectedLines(lines)) != 1 ||
		!slices.Equal(loaded.Warnings, []Diagnostic{want}) {
		t.Fatalf(
			"a malformed line reached an adapter or lost its warning: built=%d rows=%+v warnings=%+v",
			built,
			rows,
			loaded.Warnings,
		)
	}
}

func TestReloadCarriesHistoryAcrossDisplayAndCredentialEdits(t *testing.T) {
	src := source(config.Target{
		Name:   "old",
		Addr:   "192.0.2.1",
		Params: probe.RouterOS{Host: "router", Username: "user", Password: "old"},
	})
	svc := NewService(Ports{NewPinger: fakePingers(nil), LoadConfig: src.load, Host: capable})
	s, _ := openSession(t, svc, false)
	before := s.gen.rows[0]
	before.target.Consume(probe.SuccessResult(8))

	src.cfg.Lines[0] = config.Target{
		Name: "new",
		Addr: "192.0.2.1",
		Params: probe.RouterOS{
			Host:     "router",
			Username: "user",
			Password: "rotated",
			Scheme:   "https",
		},
	}

	reload(s)

	after := s.gen.rows[0]
	if after.target == before.target || after.target.Name() != "new" ||
		after.target.Snapshot().Snt != 1 || after.target.Snapshot().ID != before.target.Snapshot().ID {
		t.Fatalf(
			"reloaded display/credentials lost current values or history: before=%+v after=%+v",
			before,
			after,
		)
	}
}

// Fixing a rejected row starts monitoring with fresh statistics.
func TestReloadReplacesFixedPlaceholder(t *testing.T) {
	bad := []string{"1.1.1.1"}
	src := source(config.Target{Name: "web", Addr: bad[0]})
	svc := NewService(Ports{NewPinger: fakePingers(&bad), LoadConfig: src.load, Host: capable})

	s, _ := openSession(t, svc, false)
	if len(monitoredRows(s.Table())) != 0 || len(rejectedLines(s.Table())) != 1 {
		t.Fatal("failed row is active")
	}

	bad = nil

	out := reload(s)
	if out.Reload == nil || !out.Reload.Applied || len(rejectedLines(s.Table())) != 0 ||
		len(monitoredRows(s.Table())) != 1 ||
		monitoredRows(s.Table())[0].Target.Len() != 0 {
		t.Fatalf("fixed row: %+v", out.Reload)
	}
}

// A reload keeps the statistics and history of every target whose identity (Key) is
// unchanged, builds fresh the ones that are new or whose attributes changed, starts the
// next generation and reports the reread display directives.
func TestReloadCarriesOverStatsByKey(t *testing.T) {
	src := source(
		config.Target{
			Name:   "kept",
			Addr:   "8.8.8.8",
			Params: probe.Direct{},
		},
		config.Target{
			Name:   "moved",
			Addr:   "1.1.1.1",
			Params: probe.Direct{},
		},
	)
	svc := NewService(Ports{NewPinger: fakePingers(nil), LoadConfig: src.load, Host: capable})
	s, _ := openSession(t, svc, false)

	old := slices.Clone(s.gen.rows)
	for _, r := range old {
		r.target.Consume(probe.SuccessResult(5))
	}

	oldGen := s.gen.id

	// "moved" changes its address, so its identity changes; a separator is added.
	src.cfg = config.Config{
		Lines: []config.Line{
			config.Separator{Label: "Reloaded group"},
			config.Target{Name: "moved", Addr: "1.0.0.1", Params: probe.Direct{}},
			config.Target{Name: "kept", Addr: "8.8.8.8", Params: probe.Direct{}},
		},
		Display: config.Display{Columns: map[string]bool{"MIN": false}},
	}

	out := reload(s)
	if out.Reload == nil || !out.Reload.Applied || len(out.Tasks) != 1 {
		t.Fatalf("reload = %+v, want it applied with the next round scheduled", out)
	}

	got := s.gen.rows
	if len(got) != 2 || len(s.Table()) != 3 ||
		s.Table()[0] != (Separator{Label: "Reloaded group"}) {
		t.Fatalf("want two monitored targets and one visual separator, got %+v", got)
	}

	if moved := got[0].target; moved == old[1].target || moved.Snapshot().Snt != 0 {
		t.Errorf(
			"a target whose address changed must start fresh, got Snt=%d",
			moved.Snapshot().Snt,
		)
	}

	if kept := got[1].target; kept == old[0].target || kept.Snapshot().Snt != 1 ||
		kept.Snapshot().Len() != 1 {
		t.Errorf(
			"a fresh target must keep matching stats and history; got Snt=%d (same object: %v)",
			kept.Snapshot().Snt,
			kept == old[0].target,
		)
	}

	if s.gen.id != oldGen+1 {
		t.Errorf("generation = %d, want %d", s.gen.id, oldGen+1)
	}

	if v, ok := out.Reload.Display.Columns["MIN"]; !ok || v {
		t.Errorf("Columns = %v, want the reloaded config's MIN=off", out.Reload.Display.Columns)
	}

	if !slices.Equal(out.Inflight, []bool{false, false, false}) {
		t.Errorf("Inflight = %v, want the new generation's rows, none probing yet", out.Inflight)
	}
}

// record folds the result into the target and logs the values as they stand after the
// fold, so the logged state, rtt, avg and snt include this probe.
func TestRecordFoldsAndLogs(t *testing.T) {
	log := &recordingLog{}
	s := newSession(t.Context(), sessionPorts{log: log}, nil, nil, false)
	t.Cleanup(s.Close)

	tg := monitor.NewTarget("host#1", "host", "8.8.8.8")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	s.record(tg, probe.SuccessResult(4), now)
	s.record(tg, probe.FailedResult(), now)

	if tg.Snapshot().Snt != 2 || tg.Snapshot().Loss != 1 {
		t.Fatalf(
			"target after two records: Snt=%d Loss=%d, want 2/1",
			tg.Snapshot().Snt,
			tg.Snapshot().Loss,
		)
	}

	want := []logLine{
		{id: "host#1", name: "host", state: monitor.Up, rtt: 4, avg: 4, snt: 1, now: now},
		{id: "host#1", name: "host", state: monitor.Down, rtt: 0, avg: 4, snt: 2, now: now},
	}
	if !slices.Equal(log.lines, want) {
		t.Errorf("logged %+v, want %+v", log.lines, want)
	}
}

// Without a ResultLog (no -l), record still folds the result and logs nothing.
func TestRecordWithoutLog(t *testing.T) {
	s := newSession(t.Context(), sessionPorts{}, nil, nil, false)
	t.Cleanup(s.Close)

	tg := monitor.NewTarget("host#1", "host", "8.8.8.8")

	s.record(tg, probe.SuccessResult(4), time.Now())

	if tg.Snapshot().Snt != 1 {
		t.Errorf("Snt = %d, want 1", tg.Snapshot().Snt)
	}
}

func TestRejectedRowsAreNeverProbedOrLogged(t *testing.T) {
	for _, async := range []bool{false, true} {
		log := &recordingLog{}
		svc := NewService(
			Ports{
				NewPinger:  fakePingers(&[]string{"192.0.2.1"}),
				LoadConfig: source(config.Target{Name: "bad", Addr: "192.0.2.1"}).load,
				Host:       capable,
				Log:        log,
			},
		)
		s, _ := openSession(t, svc, async)

		out := s.Update(mustEvent(t, s.Start()), time.Now())
		if len(monitoredRows(s.Table())) != 0 || len(rejectedLines(s.Table())) != 1 ||
			len(out.Tasks) != 1 ||
			!slices.Equal(out.Inflight, []bool{false}) ||
			out.Recorded != nil ||
			len(log.lines) != 0 {
			t.Fatalf("rejected row scheduled: %+v", out)
		}

		s.Close()

		if event, ok := out.Tasks[0](); ok {
			t.Fatalf("expected canceled round timer, got %T", event)
		}
	}
}

// Equal configurations represent independent rows, including after adding or
// removing occurrences during reload. Adapter replacement must not reset history.
func TestReloadDuplicateRowsKeepIndependentHistories(t *testing.T) {
	built := 0
	svc := NewService(Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			built++
			id := built

			return pingerFunc(
				func(context.Context) probe.Result { return probe.SuccessResult(float64(id)) },
			), nil
		},
		Host: capable,
	})
	s := newSession(t.Context(), sessionPorts{}, nil, nil, false)
	t.Cleanup(s.Close)

	spec := config.Target{Name: "same", Addr: "192.0.2.1"}
	old, _, _ := svc.build(configOf(spec, spec))
	old[0].target.Consume(probe.SuccessResult(10))
	old[1].target.Consume(probe.SuccessResult(20))
	old[1].target.Consume(probe.SuccessResult(30))

	fresh, lines, _ := svc.build(configOf(spec, config.Separator{}, spec, spec))

	want := []Line{Monitored{}, Separator{}, Monitored{}, Monitored{}}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	// A result received while preparation was in progress must survive application.
	old[0].target.Consume(probe.SuccessResult(40))

	carry(fresh, old)

	rows := fresh
	if rows[0].target == rows[1].target || rows[1].target == rows[2].target {
		t.Fatal("duplicate rows share an entity after reload")
	}

	if rows[0].target.Snapshot().ID == rows[1].target.Snapshot().ID ||
		rows[1].target.Snapshot().ID == rows[2].target.Snapshot().ID ||
		rows[0].target.Snapshot().ID != old[0].target.Snapshot().ID ||
		rows[1].target.Snapshot().ID != old[1].target.Snapshot().ID {
		t.Fatal("duplicate rows lost their stable, separate identities")
	}

	if rows[0].target.Snapshot().Snt != 2 || rows[0].target.Snapshot().RTT != 40 ||
		rows[1].target.Snapshot().Snt != 2 ||
		rows[1].target.Snapshot().RTT != 30 ||
		rows[2].target.Snapshot().Snt != 0 {
		t.Fatal("histories were not matched by occurrence")
	}

	res := rows[0].pinger.Send(t.Context())
	if res.RTT != 3 {
		t.Fatalf("retained old adapter: RTT=%v", res.RTT)
	}

	s.record(rows[0].target, res, time.Now())

	if rows[0].target.Snapshot().Snt != 3 || rows[1].target.Snapshot().Snt != 2 {
		t.Fatal("recording one row changed another")
	}

	smaller, _, _ := svc.build(configOf(spec))
	carry(smaller, rows)

	if smaller[0].target.Snapshot().Snt != 3 {
		t.Fatal("removing duplicate lost first occurrence history")
	}
}

// monitoredRows lists the monitored rows of a table, in config order.
func monitoredRows(lines []Line) []Monitored {
	var out []Monitored

	for _, l := range lines {
		if m, ok := l.(Monitored); ok {
			out = append(out, m)
		}
	}

	return out
}

// rejectedLines lists table rows that could not be built.
func rejectedLines(lines []Line) []Rejected {
	var out []Rejected

	for _, l := range lines {
		if r, ok := l.(Rejected); ok {
			out = append(out, r)
		}
	}

	return out
}

// onlyWarning returns the single warning of ws, if there is one and it is a D.
func onlyWarning[D Diagnostic](ws []Diagnostic) (D, bool) {
	var d D
	if len(ws) != 1 {
		return d, false
	}

	d, ok := ws[0].(D)

	return d, ok
}

// The factory is told each row's ID, so an adapter keeping state on a remote host can key
// it by the row: the same across a reload, and distinct among duplicate rows.
func TestPingerFactoryIsToldTheRow(t *testing.T) {
	var told []string

	svc := NewService(Ports{
		NewPinger: func(_ probe.Plan, row string) (probe.Pinger, error) {
			told = append(told, row)

			return downPinger{}, nil
		},
		LoadConfig: source(
			config.Target{Name: "a", Addr: "192.0.2.1"},
			config.Target{Name: "b", Addr: "192.0.2.1"},
		).load,
		Host: capable,
	})
	s, _ := openSession(t, svc, false)
	reload(s)

	rows := monitoredRows(s.Table())
	want := []string{rows[0].Target.ID, rows[1].Target.ID, rows[0].Target.ID, rows[1].Target.ID}

	if !slices.Equal(told, want) || told[0] == told[1] {
		t.Fatalf("factory was told rows %q, want %q", told, want)
	}
}
