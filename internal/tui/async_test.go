package tui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/monitor"
	"github.com/yuu61/deadman/internal/ping"
)

// sleepPinger records when each probe begins and then blocks for d, so a test can
// tell whether a round fired its probes concurrently or one after another.
type sleepPinger struct {
	d      time.Duration
	record func()
}

func (p sleepPinger) Send(ctx context.Context) ping.Result {
	p.record()
	time.Sleep(p.d)
	return ping.Result{Success: true, Code: ping.Success, RTT: 1}
}

func runRoundAndMeasureSpread(t *testing.T, async bool) time.Duration {
	t.Helper()
	const probe = 300 * time.Millisecond

	var mu sync.Mutex
	var starts []time.Time
	record := func() {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
	}

	rows := make([]Row, 3)
	for i := range rows {
		tg, err := monitor.NewTarget("t", ping.Spec{Addr: "1.2.3.4", Relay: map[string]string{}}, 10)
		if err != nil {
			t.Fatal(err)
		}
		tg.Pinger = sleepPinger{d: probe, record: record}
		rows[i] = Row{Target: tg}
	}

	m := Model{rows: rows, opts: Options{Async: async, Scale: 10}}

	// Headless: no renderer, and an input that never returns so the program does
	// not quit on EOF. We kill it once one round has had time to fire.
	pr, pw := io.Pipe()
	defer pw.Close()
	p := tea.NewProgram(m, tea.WithoutRenderer(), tea.WithInput(pr))
	go p.Run()

	time.Sleep(3*probe + 250*time.Millisecond) // long enough for a sequential round to begin all 3 probes
	p.Kill()

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
	specs := []config.TargetSpec{
		{Name: "h1", Addr: "1.2.3.4", Relay: map[string]string{}},
		{Name: "h2", Addr: "5.6.7.8", Relay: map[string]string{}},
	}
	m, err := New(specs, Options{Scale: 10, Async: true})
	if err != nil {
		t.Fatal(err)
	}
	m, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40}, roundStartMsg{gen: 0})

	if !m.inflight[0] || !m.inflight[1] {
		t.Fatalf("both targets should be in-flight after the round starts: %v", m.inflight)
	}
	if c := strings.Count(out, ">"); c < 2 {
		t.Errorf("expected a 'pinging' arrow on both async rows, found %d:\n%s", c, out)
	}

	m, _ = drive(t, m, pingResultMsg{idx: 0, gen: 0, target: m.rows[0].Target, res: ping.Result{Success: true, Code: ping.Success, RTT: 5}})
	if m.inflight[0] {
		t.Errorf("inflight[0] should clear once its result lands")
	}
	if !m.inflight[1] {
		t.Errorf("inflight[1] should still be set (its probe has not returned)")
	}
}

// In async mode a round must fire all probes at once: their start times cluster.
func TestAsyncRoundFiresConcurrently(t *testing.T) {
	spread := runRoundAndMeasureSpread(t, true)
	t.Logf("async start-time spread across 3 probes: %v", spread)
	if spread > 150*time.Millisecond {
		t.Errorf("async probes did not start concurrently (spread %v); expected near-simultaneous", spread)
	}
}

// In sync mode probes are staggered (one after another, ~probe apart).
func TestSyncRoundFiresSequentially(t *testing.T) {
	spread := runRoundAndMeasureSpread(t, false)
	t.Logf("sync start-time spread across 3 probes: %v", spread)
	if spread < 300*time.Millisecond {
		t.Errorf("sync probes appear concurrent (spread %v); expected staggered", spread)
	}
}
