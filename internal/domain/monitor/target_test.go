package monitor

import (
	"math"
	"strconv"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestSnapshotRemainsIndependentAcrossMutationAndRingWrap(t *testing.T) {
	target := NewTarget("row#1", "original", "192.0.2.1")
	target.Consume(probe.SuccessResult(7))
	view := target.Snapshot()
	view.Name = "changed"
	view.Addr = "changed"
	view.Snt = 100

	if current := target.Snapshot(); current.ID != "row#1" || current.Name != "original" ||
		current.Addr != "192.0.2.1" || current.Snt != 1 {
		t.Fatalf("snapshot mutation changed entity: %+v", current)
	}

	for range historyCap + 1 {
		target.Consume(probe.FailedResult())
	}

	target.Refresh()

	if view.Len() != 1 || view.At(0).RTT != 7 {
		t.Fatal("later entity mutation changed the retained snapshot")
	}
}

func TestConsume(t *testing.T) {
	tg := &Target{}
	tg.Consume(probe.Result{Code: probe.Success, RTT: 10})
	tg.Consume(probe.Result{Code: probe.Failed})
	tg.Consume(probe.Result{Code: probe.Success, RTT: 30})

	if tg.Snapshot().Snt != 3 {
		t.Errorf("Snt = %d, want 3", tg.Snapshot().Snt)
	}

	if tg.Snapshot().Loss != 1 {
		t.Errorf("Loss = %d, want 1", tg.Snapshot().Loss)
	}

	if !(math.Abs(tg.Snapshot().LossRate-100.0/3.0) <= 1e-9) {
		t.Errorf("LossRate = %v, want %v", tg.Snapshot().LossRate, 100.0/3.0)
	}
	// RTT average uses the two successful observations, not the failed send.
	if !(math.Abs(tg.Snapshot().Avg-20) <= 1e-9) {
		t.Errorf("Avg = %v, want 20", tg.Snapshot().Avg)
	}

	if tg.Snapshot().State != Up {
		t.Errorf("State = %v, want Up", tg.Snapshot().State)
	}
	// min/max over the successful RTTs (10, 30).
	if tg.Snapshot().Min != 10 || tg.Snapshot().Max != 30 {
		t.Errorf("Min/Max = %v/%v, want 10/30", tg.Snapshot().Min, tg.Snapshot().Max)
	}
	// Jitter is the RFC 3550 EWMA of |ΔRTT| over consecutive successes: the first
	// success (RTT 10) only seeds prevRTT; the second (RTT 30) gives
	// Jit = 0 + (|30-10| - 0)/16 = 1.25. The failure between them is skipped.
	if !(math.Abs(tg.Snapshot().Jit-1.25) <= 1e-9) {
		t.Errorf("Jit = %v, want 1.25", tg.Snapshot().Jit)
	}
	// History is newest-first: the last result was RTT 30.
	if got := tg.Snapshot().At(0); !got.IsSuccess() || got.RTT != 30 {
		t.Errorf("At(0) = %+v, want the RTT 30 success", got)
	}

	if tg.Snapshot().Len() != 3 {
		t.Errorf("history length = %d, want 3", tg.Snapshot().Len())
	}
}

func TestRTTAverageIgnoresLossPosition(t *testing.T) {
	for _, results := range [][]probe.Result{
		{probe.SuccessResult(10), probe.FailedResult(), probe.SuccessResult(30)},
		{probe.SuccessResult(10), probe.SuccessResult(30), probe.FailedResult()},
	} {
		target := NewTarget("h#1", "h", "192.0.2.1")
		for _, result := range results {
			target.Consume(result)
		}

		if got := target.Snapshot().Avg; got != 20 {
			t.Fatalf("Avg = %v for %+v, want 20", got, results)
		}
	}
}

func TestRTTStatisticsEdgeCases(t *testing.T) {
	for _, c := range []struct {
		name    string
		results []probe.Result
		want    Stats
	}{
		{
			"zero is an observation",
			[]probe.Result{probe.SuccessResult(0), probe.SuccessResult(16)},
			Stats{State: Up, Snt: 2, RTT: 16, Max: 16, Tot: 16, Avg: 8, Jit: 1},
		},
		{
			"decreasing RTT",
			[]probe.Result{probe.SuccessResult(30), probe.SuccessResult(10)},
			Stats{State: Up, Snt: 2, RTT: 10, Min: 10, Max: 30, Tot: 40, Avg: 20, Jit: 1.25},
		},
		{
			"equal RTTs",
			[]probe.Result{probe.SuccessResult(10), probe.SuccessResult(10), probe.SuccessResult(10)},
			Stats{State: Up, Snt: 3, RTT: 10, Min: 10, Max: 10, Tot: 30, Avg: 10},
		},
		{
			"jitter decays",
			[]probe.Result{probe.SuccessResult(10), probe.SuccessResult(26), probe.SuccessResult(26)},
			Stats{State: Up, Snt: 3, RTT: 26, Min: 10, Max: 26, Tot: 62, Avg: 62.0 / 3, Jit: 0.9375},
		},
		{
			// The first success seeds min, max and the jitter's predecessor even when
			// losses came before it, so they are not folded against zero.
			"losses before the first success",
			[]probe.Result{
				probe.FailedResult(), probe.SuccessResult(10),
				probe.FailedResult(), probe.SuccessResult(26),
			},
			Stats{
				State: Up, Snt: 4, Loss: 2, LossRate: 50,
				RTT: 26, Min: 10, Max: 26, Tot: 36, Avg: 18, Jit: 1,
			},
		},
		{
			// With nothing sent there is no loss rate to divide out: it stays 0, not NaN.
			"nothing observed yet",
			[]probe.Result{probe.RelayFailedResult(), probe.UnavailableResult()},
			Stats{State: Unknown},
		},
		{
			"failures around zero RTT",
			[]probe.Result{
				probe.FailedResult(), probe.UnavailableResult(), probe.SuccessResult(0),
				probe.FailedResult(), probe.RelayFailedResult(), probe.SuccessResult(16),
			},
			Stats{State: Up, Snt: 4, Loss: 2, LossRate: 50, RTT: 16, Max: 16, Tot: 16, Avg: 8, Jit: 1},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			target := NewTarget("row", "host", "192.0.2.1")
			for _, result := range c.results {
				target.Consume(result)
			}

			// Literal expectations also reject NaN in any statistic.
			if got := target.Snapshot().Stats; got != c.want {
				t.Fatalf("statistics = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestUnobservableProbeDoesNotBecomeTargetLoss(t *testing.T) {
	target := NewTarget("host#1", "host", "192.0.2.1")
	target.Consume(probe.SuccessResult(10))

	for _, res := range []probe.Result{
		{Code: probe.RelayTimeout},
		{Code: probe.RelayFailed},
		probe.UnavailableResult(),
	} {
		target.Consume(res)

		got := target.Snapshot()
		if got.State != Unknown || got.Snt != 1 || got.Loss != 0 || got.LossRate != 0 ||
			got.RTT != 0 {
			t.Fatalf("unobservable result %+v changed target statistics: %+v", res, got.Stats)
		}

		if got.At(0).Code != res.Code {
			t.Fatalf("latest history = %+v, want %+v", got.At(0), res)
		}
	}

	target.Consume(probe.FailedResult())

	got := target.Snapshot()
	if got.State != Down || got.Snt != 2 || got.Loss != 1 || got.LossRate != 50 {
		t.Fatalf("target non-response was not counted: %+v", got.Stats)
	}
}

// TestHistoryRingWrap pins the bounded-ring contract: pushing more than historyCap
// results caps Len at historyCap, keeps the newest historyCap newest-first via At, and
// evicts the oldest. This guards the ring index math in Consume/At.
func TestHistoryRingWrap(t *testing.T) {
	tg := &Target{}

	const extra = 5
	for i := range historyCap + extra {
		tg.Consume(probe.Result{Code: probe.Success, RTT: float64(i)})
	}

	view := tg.Snapshot()
	if view.Len() != historyCap {
		t.Fatalf("Len = %d, want %d", view.Len(), historyCap)
	}

	// Newest first: At(0) is the last pushed; At(Len-1) is the oldest still retained
	// (RTT extra, since the first `extra` results were evicted).
	if got := view.At(0).RTT; got != float64(historyCap+extra-1) {
		t.Errorf("At(0).RTT = %v, want %v", got, float64(historyCap+extra-1))
	}

	if got := view.At(historyCap - 1).RTT; got != float64(extra) {
		t.Errorf("At(Len-1).RTT = %v, want %v", got, float64(extra))
	}
}

func TestRefresh(t *testing.T) {
	tg := &Target{}
	tg.Consume(probe.Result{Code: probe.Success, RTT: 5})
	tg.Consume(probe.Result{Code: probe.Success, RTT: 25})
	tg.Refresh()

	if tg.Snapshot().Stats != (Stats{}) || tg.Snapshot().Len() != 0 {
		t.Errorf("after Refresh: %+v histLen=%d", tg, tg.Snapshot().Len())
	}
	// The previous sample is internal state, beyond the public statistics above.
	if tg.prevRTT != 0 {
		t.Errorf("after Refresh: prevRTT=%v, want 0", tg.prevRTT)
	}
}

// A target keeps the identity, name and address it was built with; they reach every
// snapshot, and a refresh clears only the statistics.
func TestNewTargetKeepsIdentity(t *testing.T) {
	target := NewTarget("row#2", "web", "example.com")
	target.Consume(probe.SuccessResult(3))
	target.Refresh()

	view := target.Snapshot()
	if target.Name() != "web" || view.ID != "row#2" ||
		view.Name != "web" || view.Addr != "example.com" || view.Snt != 0 {
		t.Errorf("target = %q, snapshot = %+v", target.Name(), view)
	}
}

// A reader that follows a snapshot by Advance sees what a fresh Snapshot shows: the same
// statistics and the same results, newest first, bounded like the entity's history at
// every step, including the one that first reaches the bound.
func TestAdvanceFollowsTheTarget(t *testing.T) {
	for _, seed := range []int{0, 1, historyCap - 1, historyCap, historyCap + 5} {
		t.Run(strconv.Itoa(seed), func(t *testing.T) { assertAdvanceFollowsTarget(t, seed) })
	}
}

func assertAdvanceFollowsTarget(t *testing.T, seed int) {
	t.Helper()

	target := NewTarget("row#1", "web", "example.com")

	history := make([]probe.Result, 0, seed+3*historyCap+40)
	for i := range seed {
		result := probe.SuccessResult(float64(i))
		history = append(history, result)
		target.Consume(result)
	}

	view := target.Snapshot()

	for i := range 3*historyCap + 40 {
		res := probe.SuccessResult(float64(i))
		if i%3 == 0 {
			res = probe.FailedResult()
		}

		view = view.Advance(target.Consume(res))
		history = append(history, res)

		if want := min(seed+i+1, historyCap); view.Len() != want {
			t.Fatalf(
				"after %d results the advanced history holds %d, want %d",
				i+1,
				view.Len(),
				want,
			)
		}

		want := target.Snapshot()
		if view.Stats != want.Stats || view.Len() != want.Len() {
			t.Fatalf("advanced %+v (len %d), snapshot %+v (len %d)",
				view.Stats, view.Len(), want.Stats, want.Len())
		}

		for j := range want.Len() {
			expected := history[len(history)-1-j]
			if view.At(j) != expected || want.At(j) != expected {
				t.Fatalf("after %d advances At(%d) = %+v, snapshot has %+v, want %+v",
					i+1, j, view.At(j), want.At(j), expected)
			}
		}
	}
}

func TestAdvanceDoesNotChangeEntityOrOtherSnapshots(t *testing.T) {
	target := NewTarget("row#1", "web", "example.com")

	const seed = historyCap + 5

	for i := range seed {
		target.Consume(probe.SuccessResult(float64(i)))
	}

	view, retained := target.Snapshot(), target.Snapshot()
	for range 2 * historyCap {
		view = view.Advance(Reading{Stats: view.Stats, Result: probe.FailedResult()})
	}

	current := target.Snapshot()

	for i := range historyCap {
		want := probe.SuccessResult(float64(seed - 1 - i))
		if retained.At(i) != want || current.At(i) != want {
			t.Fatalf("Advance changed another history at %d", i)
		}

		if view.At(i) != probe.FailedResult() {
			t.Fatalf("advanced At(%d) = %+v, want failure", i, view.At(i))
		}
	}
}
