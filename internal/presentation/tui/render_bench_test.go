package tui

import (
	"fmt"
	"testing"

	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Cover both long same-level runs and a separate style for every cell.
func BenchmarkResultBar(b *testing.B) {
	for _, profile := range []termenv.Profile{termenv.Ascii, termenv.TrueColor} {
		for _, logIdx := range []int{0, 1} {
			for _, failureEvery := range []int{0, 2} {
				name := fmt.Sprintf(
					"%s/log=%d/alternating=%t",
					profile.Name(),
					logIdx,
					failureEvery > 0,
				)
				b.Run(name, func(b *testing.B) {
					withColor(b, profile, true)

					m := Model{
						layout:    layout{resW: resultHistoryCap},
						viewPrefs: viewPrefs{scale: 10, logIdx: logIdx},
					}
					snapshot := benchmarkHistory(failureEvery)

					b.ReportAllocs()

					for b.Loop() {
						m.resultBar(snapshot)
					}
				})
			}
		}
	}
}

// A full visible table measures whether bar changes help the complete redraw too.
func BenchmarkView(b *testing.B) {
	for _, profile := range []termenv.Profile{termenv.Ascii, termenv.TrueColor} {
		b.Run(profile.Name(), func(b *testing.B) {
			withColor(b, profile, true)

			m := Model{
				layout:    layout{width: 200, height: 40},
				viewPrefs: viewPrefs{scale: 10, cols: 1},
			}

			plan, err := probe.Compile(probe.Spec{Addr: "192.0.2.1", Params: probe.Direct{}})
			if err != nil {
				b.Fatal(err)
			}

			for i := range 24 {
				snapshot := benchmarkHistory(2 * ((i + 1) % 2))
				snapshot.Name, snapshot.Addr = fmt.Sprintf("host-%d", i), "192.0.2.1"
				m.lines = append(m.lines, monitoring.Monitored{Target: snapshot, Plan: plan})
				m.labels = append(m.labels, "icmp")
			}

			m = m.recalcWidths()

			b.ReportAllocs()

			for b.Loop() {
				m.View()
			}
		})
	}
}

func benchmarkHistory(failureEvery int) monitor.Snapshot {
	target := monitor.NewTarget("row#1", "web", "example.com")

	for i := range resultHistoryCap {
		result := probe.SuccessResult(35)
		if failureEvery > 0 && i%failureEvery == 0 {
			result = probe.FailedResult()
		}

		target.Consume(result)
	}

	return target.Snapshot()
}
