package monitor

import (
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Measure the steady state after the history reaches its bound.
func BenchmarkSnapshotAdvance(b *testing.B) {
	target := NewTarget("row#1", "web", "example.com")
	for range historyCap {
		target.Consume(probe.SuccessResult(5))
	}

	view := target.Snapshot()
	reading := target.Consume(probe.SuccessResult(10))

	b.ReportAllocs()

	for b.Loop() {
		view = view.Advance(reading)
	}

	if view.Len() != historyCap || view.At(0) != reading.Result {
		b.Fatal("advanced history lost the latest result")
	}
}
