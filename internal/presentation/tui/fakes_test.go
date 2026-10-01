package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// stubHost reports host capabilities for the tui tests: everything available unless a
// test turns direct ICMP off, so the startup warnings stay deterministic whatever the
// runner's CAP_NET_RAW, ping_group_range, platform or rp_filter.
type stubHost struct{ noDirectICMP bool }

func (stubHost) Platform() probe.OS { return probe.OSLinux }

func (h stubHost) DirectICMPAvailable() bool { return !h.noDirectICMP }
func (stubHost) RawICMPAvailable() bool      { return true }
func (stubHost) RPFilterStrict() bool        { return false }

// testSource stands in for the config file: a test may change cfg between reads, like
// editing the file, and a non-nil err makes the reads fail, like a missing file.
type testSource struct {
	cfg config.Config
	err error
}

func (s *testSource) load(context.Context) (config.Config, error) {
	if s.err != nil {
		return config.Config{}, s.err
	}

	return s.cfg, nil
}

// errNoConfig is testSource's read error for a config file that does not exist.
var errNoConfig = errors.New("open deadman.conf: no such file or directory")

// testOptions is the tests' shorthand for how a model starts: the display flags and host
// facts cmd/deadman would pass (-a, -b, -s, -c, -g), and the display directives of the
// config the session opens with ("precision", "columns").
type testOptions struct {
	Async       bool
	Blink       bool
	Scale       float64 // -s; 0 = not given.
	Cols        int     // -c; 0 = not given.
	Glyph       string  // -g; "" = not given.
	Precision   string  // config "precision".
	Columns     map[string]bool
	Hostname    string
	HostAddress string
}

// options is the Options cmd/deadman would pass for o.
func (o testOptions) options() Options {
	return Options{
		Hostname:    o.Hostname,
		HostAddress: o.HostAddress,
		Blink:       o.Blink,
		Display: Flags{
			Scale:    o.Scale,
			ScaleSet: o.Scale != 0,
			Glyph:    o.Glyph,
			Cols:     o.Cols,
			ColsSet:  o.Cols != 0,
		},
	}
}

// sourceOf is a config listing lines with o's display directives.
func sourceOf(lines []config.Line, o testOptions) *testSource {
	return &testSource{cfg: config.Config{
		Lines:   lines,
		Display: config.Display{Columns: o.Columns, Precision: o.Precision},
	}}
}

// noWait is the tests' monitoring clock: every wait between probes and rounds is over at
// once, unless the session has ended.
func noWait(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }

// replies scripts the probes of a test's session: each probe of a destination takes the
// next reply queued for it, and a probe with none stays in flight until one is queued or
// its generation ends. A probe of an ended generation never takes a reply, so a reply
// queued after a reload goes to the new generation's probe.
type replies struct {
	mu     sync.Mutex
	queued map[string][]probe.Result
	wake   chan struct{} // closed and replaced whenever a reply is queued.
}

func newReplies() *replies {
	return &replies{queued: map[string][]probe.Result{}, wake: make(chan struct{})}
}

// queue has the next probe of dest take res.
func (r *replies) queue(dest string, res probe.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.queued[dest] = append(r.queued[dest], res)
	close(r.wake)
	r.wake = make(chan struct{})
}

// take waits for the next reply to dest, or reports false once ctx has ended. The check of
// ctx and the taking of a reply are one step, so a probe canceled before a reply was
// queued never takes it.
func (r *replies) take(ctx context.Context, dest string) (probe.Result, bool) {
	for {
		r.mu.Lock()

		if ctx.Err() != nil {
			r.mu.Unlock()

			return probe.Result{}, false
		}

		if q := r.queued[dest]; len(q) > 0 {
			r.queued[dest] = q[1:]
			r.mu.Unlock()

			return q[0], true
		}

		wake := r.wake
		r.mu.Unlock()

		select {
		case <-wake:
		case <-ctx.Done():
		}
	}
}

// pinger is the PingerFactory of a scripted session.
func (r *replies) pinger(plan probe.Plan, _ string) (probe.Pinger, error) {
	return scriptedPinger{replies: r, dest: plan.Destination().String()}, nil
}

// scriptedPinger answers each probe with the next reply queued for its destination.
type scriptedPinger struct {
	replies *replies
	dest    string
}

func (p scriptedPinger) Send(ctx context.Context) probe.Result {
	res, ok := p.replies.take(ctx, p.dest)
	if !ok {
		return probe.FailedResult() // the generation ended; the session discards it.
	}

	return res
}

// scripts are the replies of each session testService builds, found by its service: a
// *monitoring.Service maps to its *replies.
var scripts sync.Map

// testService wires the application service to fakes: every target is probed by a
// scripted pinger that never touches the network, the config is read from src, host
// reports capabilities, and the waits between probes and rounds are over at once.
func testService(host stubHost, src *testSource) *monitoring.Service {
	r := newReplies()
	svc := monitoring.NewService(monitoring.Ports{
		NewPinger:  r.pinger,
		LoadConfig: src.load,
		Host:       host,
		Wait:       noWait,
	})
	scripts.Store(svc, r)

	return svc
}

// program runs a model's commands the way the Bubble Tea program would, each on its own
// goroutine, and hands what they send back to a test that pumps it. Only the pump updates
// the model, so between pumps the model changes only by the messages a test drives.
type program struct {
	replies *replies // nil for a session a test wired without scripted replies.
	sent    chan tea.Msg
}

// sentDepth bounds the messages waiting for a pump. Commands finishing after their test
// (a probe released by Close) send into it too, so they never block.
const sentDepth = 1024

// programs are the programs openModel starts, found by the model's session: a
// *monitoring.Session maps to its *program.
var programs sync.Map

// programOf is the program running m's commands.
func programOf(t *testing.T, m Model) *program {
	t.Helper()

	p, ok := programs.Load(m.session)
	if !ok {
		t.Fatal("the model was not opened by openModel")
	}

	prog, ok := p.(*program)
	if !ok {
		t.Fatalf("programs holds %T", p)
	}

	return prog
}

// run runs cmd in the background, as the Bubble Tea program would.
func (p *program) run(cmd tea.Cmd) {
	if cmd != nil {
		go func() { p.sent <- cmd() }()
	}
}

// pump delivers what the commands send back to m, and runs the commands m returns, until
// done(m) holds.
func (p *program) pump(t *testing.T, m Model, done func(Model) bool) Model {
	t.Helper()

	timeout := time.After(5 * time.Second)

	for !done(m) {
		select {
		case msg := <-p.sent:
			switch msg := msg.(type) {
			case nil: // a canceled task.
			case tea.BatchMsg:
				for _, cmd := range msg {
					p.run(cmd)
				}
			default:
				next, cmd := m.Update(msg)
				m = asModel(t, next)

				p.run(cmd)
			}
		case <-timeout:
			t.Fatal("monitoring did not reach the expected state")
		}
	}

	return m
}

// answer is a test message: the monitored row at table line row answers its next probe
// with res. drive queues the reply and pumps the monitoring until the row records it. The
// rows of a test that answers need distinct addresses, and a row whose history is full
// (historyCap results) cannot show that it recorded one more.
type answer struct {
	row int
	res probe.Result
}

// until is a test message: drive pumps the monitoring until the model satisfies it.
type until func(Model) bool

// success answers row 0's next probe with a reply taking rtt milliseconds.
func success(rtt float64) answer { return answer{row: 0, res: probe.SuccessResult(rtt)} }

// failure answers row 0's next probe with no reply.
func failure() answer { return answer{row: 0, res: probe.FailedResult()} }

// answer queues a's reply and pumps m until its row has recorded it.
func (p *program) answer(t *testing.T, m Model, a answer) Model {
	t.Helper()

	if p.replies == nil {
		t.Fatal("the session was not wired with scripted replies")
	}

	row, ok := m.rows()[a.row].(monitoring.Monitored)
	if !ok {
		t.Fatalf("line %d is %T, not a monitored row", a.row, m.rows()[a.row])
	}

	recorded := row.Target.Len()
	if recorded >= resultHistoryCap {
		t.Fatalf("line %d's history is full; a pump cannot see it record more", a.row)
	}

	p.replies.queue(row.Plan.Destination().String(), a.res)

	return p.pump(t, m, func(m Model) bool {
		r, ok := m.rows()[a.row].(monitoring.Monitored)

		return ok && r.Target.Len() > recorded
	})
}

// resultHistoryCap is how many results a row's history keeps (monitor's historyCap).
const resultHistoryCap = 256

// asModel is next as a Model, failing the test otherwise.
func asModel(t *testing.T, next tea.Model) Model {
	t.Helper()

	m, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}

	return m
}

// openModel opens a session on svc as cmd/deadman does and builds the model over it,
// starting its program and closing it when the test ends.
func openModel(t *testing.T, svc *monitoring.Service, o testOptions) Model {
	t.Helper()

	session, loaded, err := svc.Open(t.Context(), o.Async)
	if err != nil {
		t.Fatal(err)
	}

	m := New(session, loaded, o.options())
	t.Cleanup(m.Close)

	prog := &program{sent: make(chan tea.Msg, sentDepth)}
	if r, ok := scripts.Load(svc); ok {
		prog.replies, ok = r.(*replies)
		if !ok {
			t.Fatalf("scripts holds %T", r)
		}
	}

	programs.Store(session, prog)
	t.Cleanup(func() {
		programs.Delete(session)
		scripts.Delete(svc)
	})
	prog.run(m.Init())

	return m
}

// newModel builds a model over lines on a fake service whose host has every capability.
// It is the unsized sibling of sizedModel (which also feeds a WindowSizeMsg).
func newModel(t *testing.T, lines []config.Line, o testOptions) Model {
	t.Helper()

	return newNotedModel(t, lines, nil, o)
}

// newNotedModel is newModel over a config whose parser noted how some lines were written.
func newNotedModel(t *testing.T, lines []config.Line, notes []config.Note, o testOptions) Model {
	t.Helper()

	src := sourceOf(lines, o)
	src.cfg.Notes = notes

	return openModel(t, testService(stubHost{}, src), o)
}

// snapshotAt is the snapshot of the monitored row at line i, failing the test when that
// line is not a monitored row.
func snapshotAt(t *testing.T, m Model, i int) monitor.Snapshot {
	t.Helper()

	r, ok := m.rows()[i].(monitoring.Monitored)
	if !ok {
		t.Fatalf("line %d is %T, not a monitored row", i, m.rows()[i])
	}

	return r.Target
}

// isRejected reports whether a line is a row that could not be built.
func isRejected(line monitoring.Line) bool {
	_, ok := line.(monitoring.Rejected)

	return ok
}
