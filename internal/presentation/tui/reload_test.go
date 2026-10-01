package tui

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func updateModel(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()

	model, cmd := m.Update(msg)

	next, ok := model.(Model)
	if !ok {
		t.Fatalf("unexpected model %T", model)
	}

	return next, cmd
}

func awaitMessage(t *testing.T, messages <-chan tea.Msg) tea.Msg {
	t.Helper()

	select {
	case msg := <-messages:
		return msg
	case <-time.After(time.Second):
		t.Fatal("reload command did not finish")

		return nil
	}
}

func TestReloadPreparationDoesNotBlockUpdates(t *testing.T) {
	for _, phase := range []string{"load", "build"} {
		t.Run(phase, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			// Use a separate signal so the blocked command can be released before
			// assertions without leaving a worker behind on a failing test.
			proceed := make(chan struct{})
			block := func() {
				close(started)

				select {
				case <-proceed:
				case <-release:
				}
			}
			spec := config.Target{Name: "kept", Addr: "192.0.2.1"}
			reads := 0
			scripted := newReplies()
			svc := monitoring.NewService(monitoring.Ports{
				NewPinger: func(s probe.Plan, _ string) (probe.Pinger, error) {
					if phase == "build" && s.Destination().String() == "192.0.2.2" {
						block()
					}

					return scripted.pinger(s, "")
				},
				LoadConfig: func(context.Context) (config.Config, error) {
					// The first read opens the session; only the reload's reread blocks.
					reads++
					if reads == 1 {
						return config.Config{Lines: []config.Line{spec}}, nil
					}

					if phase == "load" {
						block()
					}

					return config.Config{
						Lines: []config.Line{spec, config.Target{Name: "new", Addr: "192.0.2.2"}},
					}, nil
				},
				Host: stubHost{},
				Wait: noWait,
			})
			scripts.Store(svc, scripted)
			m := openModel(t, svc, testOptions{Scale: 10})
			m, command := updateModel(t, m, reloadMsg{})

			messages := make(chan tea.Msg, 1)
			go func() { messages <- command() }()

			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("reload did not start")
			}

			m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
			m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})

			m, _ = drive(t, m, success(5))
			if m.width != 100 || m.precIdx != 1 || snapshotAt(t, m, 0).Snt != 1 {
				t.Fatal("UI and monitoring did not progress during reload")
			}

			close(proceed)

			m, next := updateModel(t, m, awaitMessage(t, messages))
			if len(m.rows()) != 2 || snapshotAt(t, m, 0).Snt != 1 ||
				m.precIdx != 1 {
				t.Fatal("reload lost live history or view preferences")
			}

			// The replacement goes on monitoring the carried row. (That the old
			// generation's timers and results are discarded is the session's contract,
			// tested in monitoring.)
			programOf(t, m).run(next)

			if m, _ = drive(t, m, success(9)); snapshotAt(t, m, 0).Snt != 2 {
				t.Fatal("the replacement did not go on monitoring the carried row")
			}
		})
	}
}

// closeCounter is a never-replying adapter that counts how often it is released.
type closeCounter struct {
	closed   atomic.Int32
	released chan struct{}
}

func (*closeCounter) Send(context.Context) probe.Result { return probe.FailedResult() }
func (c *closeCounter) Close() {
	if c.closed.Add(1) == 1 && c.released != nil {
		close(c.released)
	}
}

// A real Bubble Tea program drops command results after quitting. Cleanup must not
// require another Update, unlike a test that manually delivers the late completion.
func TestProgramExitReleasesUndeliveredReload(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)

	proceed := make(chan struct{})
	adapter := &closeCounter{released: make(chan struct{})}
	reads := 0
	svc := monitoring.NewService(monitoring.Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			close(started)

			select {
			case <-proceed:
			case <-release:
			}

			return adapter, nil
		},
		LoadConfig: func(context.Context) (config.Config, error) {
			// The first read opens an empty session; the reload builds one adapter.
			reads++
			if reads == 1 {
				return config.Config{}, nil
			}

			return config.Config{
				Lines: []config.Line{config.Target{Name: "late", Addr: "192.0.2.1"}},
			}, nil
		},
		Host: stubHost{},
	})
	m := openModel(t, svc, testOptions{})
	program := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler())
	t.Cleanup(program.Kill)

	done := make(chan error, 1)
	exited := make(chan struct{})

	go func() {
		_, err := program.Run()

		close(exited)

		m.Close() // cmd/deadman's run also closes the model after Program.Run.

		done <- err
	}()

	program.Send(reloadMsg{})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reload did not start")
	}

	program.Quit()

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("program did not exit")
	}

	select {
	case <-done:
		t.Fatal("model closed before the pending reload finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(proceed)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("model did not finish cleanup")
	}

	select {
	case <-adapter.released:
	default:
		t.Fatal("undelivered reload retained its adapter")
	}
}

// countingService is testService whose every built adapter is a closeCounter, recorded in
// build order.
func countingService(src *testSource) (*monitoring.Service, *[]*closeCounter) {
	var built []*closeCounter

	svc := monitoring.NewService(monitoring.Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			p := &closeCounter{}
			built = append(built, p)

			return p, nil
		},
		LoadConfig: src.load,
		Host:       stubHost{},
	})

	return svc, &built
}

func TestReloadAppliesOnlyLatestRequest(t *testing.T) {
	src := &testSource{} // the session opens with no targets, so no adapter is built.
	svc, built := countingService(src)
	m := openModel(t, svc, testOptions{})

	src.cfg = config.Config{Lines: []config.Line{config.Target{Name: "old", Addr: "192.0.2.1"}}}
	m, firstCommand := updateModel(t, m, reloadMsg{})
	first := firstCommand()
	src.cfg = config.Config{Lines: []config.Line{config.Target{Name: "new", Addr: "192.0.2.2"}}}
	m, secondCommand := updateModel(t, m, reloadMsg{})
	m, _ = updateModel(t, m, secondCommand())

	m, next := updateModel(t, m, first)
	if snapshotAt(t, m, 0).Name != "new" || next != nil {
		t.Fatal("older completion replaced the latest config")
	}

	// The superseded preparation never runs, so its adapter is released right away; the
	// applied one stays open for the running session.
	if first, applied := (*built)[0], (*built)[1]; first.closed.Load() != 1 ||
		applied.closed.Load() != 0 {
		t.Fatalf("closed: superseded=%d applied=%d, want 1 and 0",
			first.closed.Load(), applied.closed.Load())
	}
}

func TestFailedReloadKeepsSessionRunning(t *testing.T) {
	src := sourceOf([]config.Line{config.Target{Name: "kept", Addr: "192.0.2.1"}}, testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{})

	src.err = errNoConfig
	m, command := updateModel(t, m, reloadMsg{})

	m, next := updateModel(t, m, command())
	if next != nil || !strings.Contains(strings.Join(m.warnings, " "), "reload failed") {
		t.Fatal("failed reload disrupted monitoring or hid its error")
	}

	m, _ = drive(t, m, success(5))
	if snapshotAt(t, m, 0).Snt != 1 {
		t.Fatal("monitoring stopped after failed reload")
	}
}

func TestCloseCancelsReplacementAndIgnoresReloadCompletion(t *testing.T) {
	src := sourceOf([]config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}}, testOptions{})
	svc, built := countingService(src)
	m := openModel(t, svc, testOptions{})
	original := m // run's deferred Close holds this copy throughout the program.
	m, command := updateModel(t, m, reloadMsg{})
	m, _ = updateModel(t, m, command())
	m, command = updateModel(t, m, reloadMsg{})
	late := command() // prepared before the program exits, delivered after.

	original.Close()

	if _, ok := m.session.Start()(); ok {
		t.Fatal("Close did not cancel the replacement generation")
	}

	rows := m.rows()

	m, next := updateModel(t, m, late)
	if len(m.rows()) != len(rows) || next != nil {
		t.Fatal("reload completion revived a closed model")
	}

	if last := (*built)[len(*built)-1]; last.closed.Load() != 1 {
		t.Fatalf("a completion after Close released its adapter %d times, want 1",
			last.closed.Load())
	}
}

// The spinner steps once per sync pass (at its start) and twice per async pass (start
// and completion). The session reports completion in both modes; the step is the TUI's.
func TestSpinnerStepsPerRound(t *testing.T) {
	for _, c := range []struct {
		async bool
		want  int
	}{{false, 1}, {true, 2}} {
		m := newModel(
			t,
			[]config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}},
			testOptions{Async: c.async},
		)

		// The round starts, probes its one target and completes; the pump stops at the
		// result, leaving the next round unstarted.
		m, _ = drive(t, m, success(5))

		if snapshotAt(t, m, 0).Snt != 1 || m.tick != c.want {
			t.Errorf("async=%v: after one pass Snt=%d tick=%d, want 1 and %d",
				c.async, snapshotAt(t, m, 0).Snt, m.tick, c.want)
		}
	}
}

// A reload that fails still replaces the header warnings, and so the header's height:
// the layout (the columns the height fits, the result bar's width, the scroll position)
// must follow it, as a resize or an applied reload does.
func TestFailedReloadRecomputesTheLayout(t *testing.T) {
	// Thirteen rows under a tall header overflow three columns of four rows, so the list
	// scrolls, here to its end; with the header shrunk to one warning they all fit.
	opts := testOptions{
		Scale:   10,
		Cols:    3,
		Columns: map[string]bool{"MIN": false, "MAX": false, "VIA": false},
	}
	m := sizedModel(t, 13, opts, 320, 12)
	m.configWarns = append(m.configWarns, "w1", "w2", "w3", "w4")

	m = m.composeWarnings().recalcWidths().clampScroll().scroll("G")
	if vp := m.scrollMetrics(); !vp.active || m.scrollTop == 0 {
		t.Fatalf("staged viewport %+v top=%d, want a list scrolled to its end", vp, m.scrollTop)
	}

	m = m.showReload(monitoring.ReloadOutcome{
		Loaded: monitoring.Loaded{
			Warnings: []monitoring.Diagnostic{monitoring.ReloadFailed{Reason: "gone"}},
		},
	})

	want := m.recalcWidths().clampScroll()
	if m.resW != want.resW || m.scrollTop != want.scrollTop ||
		m.effectiveCols() != want.effectiveCols() || m.scrollTop != 0 {
		t.Fatalf("after a failed reload resW=%d top=%d cols=%d, want %d %d %d",
			m.resW, m.scrollTop, m.effectiveCols(), want.resW, want.scrollTop, want.effectiveCols())
	}
}
