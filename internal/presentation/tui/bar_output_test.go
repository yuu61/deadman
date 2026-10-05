package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// A full history at one level should fit in one pty read rather than thousands
// of bytes of repeated SGR. Check the glyphs and width at every color depth too.
func TestLongResultBarOutput(t *testing.T) {
	for _, profile := range []termenv.Profile{termenv.Ascii, termenv.ANSI, termenv.ANSI256, termenv.TrueColor} {
		t.Run(profile.Name(), func(t *testing.T) {
			withColor(t, profile, true)

			m := Model{layout: layout{resW: resultHistoryCap}, viewPrefs: viewPrefs{scale: 10}}

			var snapshot monitor.Snapshot

			for range resultHistoryCap {
				snapshot = snapshot.Advance(monitor.Reading{Result: probe.SuccessResult(0.5)})
			}

			out := m.resultBar(snapshot)
			if got := ansi.Strip(out); got != strings.Repeat("▁", resultHistoryCap) {
				t.Errorf("bar glyphs = %q", got)
			}

			if got := lipgloss.Width(out); got != resultHistoryCap {
				t.Errorf("bar width = %d, want %d", got, resultHistoryCap)
			}

			const maxBytes = 1024
			if len(out) > maxBytes {
				t.Errorf("one-level bar uses %d bytes, want at most %d", len(out), maxBytes)
			}
		})
	}
}

// Runs must preserve newest-first order, all failure codes, color transitions,
// clipping and the reset before the following column or erased line remainder.
func TestResultBarTransitions(t *testing.T) {
	for _, profile := range []termenv.Profile{termenv.Ascii, termenv.ANSI, termenv.ANSI256, termenv.TrueColor} {
		t.Run(profile.Name(), func(t *testing.T) {
			withColor(t, profile, true)

			for _, bar := range []resultbar.Bar{resultbar.BarBlock, resultbar.BarASCII, resultbar.BarDigit} {
				t.Run(bar.String(), func(t *testing.T) {
					for _, mode := range []struct {
						name          string
						logIdx, level int
					}{
						{"linear", 0, 3},
						{"log", 1, 1},
						{"log_squared", 2, 0},
					} {
						t.Run(mode.name, func(t *testing.T) {
							assertResultBarTransitions(t, profile, bar, mode.logIdx, mode.level)
						})
					}
				})
			}
		})
	}
}

func assertResultBarTransitions(
	t *testing.T,
	profile termenv.Profile,
	bar resultbar.Bar,
	logIdx, level int,
) {
	t.Helper()

	m := Model{layout: layout{resW: 8}, viewPrefs: viewPrefs{scale: 10, bar: bar, logIdx: logIdx}}

	var snapshot monitor.Snapshot

	for _, res := range []probe.Result{
		probe.SuccessResult(5000), probe.SuccessResult(35), probe.SuccessResult(35),
		probe.SuccessResult(0.5), probe.FailedResult(), probe.UnavailableResult(),
		probe.RelayFailedResult(), probe.RelayTimeoutResult(), probe.SuccessResult(0.5),
	} {
		snapshot = snapshot.Advance(monitor.Reading{Result: res})
	}

	out := m.resultBar(snapshot)

	fast := rttStyle(bar, 0).Render(bar.GlyphAt(0))

	tail := fast + rttStyle(bar, level).Render(strings.Repeat(bar.GlyphAt(level), 2))
	if level == 0 {
		tail = rttStyle(bar, 0).Render(strings.Repeat(bar.GlyphAt(0), 3))
	}

	want := fast + styleFail.Render("ts?X") + tail
	if out != want {
		t.Errorf("bar = %q, want %q", out, want)
	}

	if profile != termenv.Ascii && !strings.HasSuffix(out, "\x1b[0m") {
		t.Error("bar leaves the terminal style active")
	}
}
