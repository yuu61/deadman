package tui

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// sleepPinger records when each probe begins and then blocks for d, so a test can
// tell whether a round fired its probes concurrently or one after another.
type sleepPinger struct {
	d      time.Duration
	record func()
}

func (p sleepPinger) Send(ctx context.Context) probe.Result {
	p.record()

	timer := time.NewTimer(p.d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return probe.UnavailableResult()
	case <-timer.C:
	}

	return probe.Result{Code: probe.Success, RTT: 1}
}

func runRoundAndMeasureSpread(t *testing.T, async bool) time.Duration {
	t.Helper()

	const probeDelay = 300 * time.Millisecond

	var (
		mu     sync.Mutex
		starts []time.Time
	)

	started := make(chan struct{})

	record := func() {
		mu.Lock()

		starts = append(starts, time.Now())
		if len(starts) == 3 {
			close(started)
		}
		mu.Unlock()
	}

	o := testOptions{Async: async, Scale: 10}
	svc := monitoring.NewService(monitoring.Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			return sleepPinger{d: probeDelay, record: record}, nil
		},
		LoadConfig: sourceOf([]config.Line{
			config.Target{Name: "a", Addr: "1.2.3.4"},
			config.Target{Name: "b", Addr: "1.2.3.4"},
			config.Target{Name: "c", Addr: "1.2.3.4"},
		}, o).load,
		Host: stubHost{},
	})
	m := openModel(t, svc, o)

	// Headless: no renderer, and an input that never returns so the program does
	// not quit on EOF. We kill it once all three probes have started.
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	p := tea.NewProgram(m, tea.WithoutRenderer(), tea.WithInput(pr))

	done := make(chan struct{})
	go func() {
		defer close(done)

		_, err := p.Run()
		if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			t.Errorf("program: %v", err)
		}
	}()

	defer func() {
		p.Kill()
		<-done
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case <-started:
	case <-timer.C:
		t.Fatal("the probes did not start")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(starts) < 3 {
		t.Fatalf("only %d of 3 probes started; cannot measure", len(starts))
	}

	first, last := starts[0], starts[0]
	for _, s := range starts[:3] {
		if s.Before(first) {
			first = s
		}

		if s.After(last) {
			last = s
		}
	}

	return last.Sub(first)
}

// In async mode, a round must visibly mark every target as "pinging" at once
// (arrow on all rows), then clear each row as its result arrives.
func TestAsyncShowsInflightArrows(t *testing.T) {
	specs := []config.Line{
		config.Target{Name: "h1", Addr: "1.2.3.4", Params: probe.Direct{}},
		config.Target{Name: "h2", Addr: "5.6.7.8", Params: probe.Direct{}},
	}

	m := newModel(t, specs, testOptions{Scale: 10, Async: true})

	m, out := drive(
		t,
		m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		until(func(m Model) bool { return slices.Contains(m.inflight, true) }),
	)

	if !m.inflight[0] || !m.inflight[1] {
		t.Fatal("both targets should be in-flight after the round starts")
	}

	if c := strings.Count(out, ">"); c < 2 {
		t.Errorf("expected a 'pinging' arrow on both async rows, found %d:\n%s", c, out)
	}

	m, _ = drive(
		t,
		m,
		success(5),
	)
	if m.inflight[0] {
		t.Error("inflight[0] should clear once its result lands")
	}

	if !m.inflight[1] {
		t.Error("inflight[1] should still be set (its probe has not returned)")
	}
}

// In async mode a round must fire all probes at once: their start times cluster.
func TestAsyncRoundFiresConcurrently(t *testing.T) {
	spread := runRoundAndMeasureSpread(t, true)
	t.Logf("async start-time spread across 3 probes: %v", spread)

	if spread > 150*time.Millisecond {
		t.Errorf(
			"async probes did not start concurrently (spread %v); expected near-simultaneous",
			spread,
		)
	}
}

// In sync mode probes are staggered (one after another, ~probeDelay apart).
func TestSyncRoundFiresSequentially(t *testing.T) {
	spread := runRoundAndMeasureSpread(t, false)
	t.Logf("sync start-time spread across 3 probes: %v", spread)

	if spread < 300*time.Millisecond {
		t.Errorf("sync probes appear concurrent (spread %v); expected staggered", spread)
	}
}
