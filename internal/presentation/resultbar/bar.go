// Package resultbar turns probe results into the RESULT bar's cells: the glyph sets a
// success can be drawn with (block / ascii / digit), the level a success sits at on the
// RTT scale (linear or logarithmic), and the X/t/s/? glyphs of a failure. The level is
// what both the glyph and the color (palette) are picked by, so the two always agree.
package resultbar

import (
	"math"
	"strings"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Bar selects the glyph set a successful probe renders as in the result bar. The zero
// value is BarBlock, so a Model or test that never picks a set keeps the block elements.
type Bar int

// Result-bar glyph sets, in the 'b'-key cycle order.
const (
	// BarBlock is the block-element bar ▁▂▃▄▅▆▇█ (the default).
	BarBlock Bar = iota
	// BarASCII is BarBlock's 8 levels and thresholds spelled in ASCII, for a terminal
	// whose font or locale cannot show block elements (e.g. the Linux virtual console's
	// Lat15 font). Only the characters change, so switching sets never re-buckets.
	BarASCII
	// BarDigit is 0-9: digit d covers [scale*d, scale*(d+1)), 9 at or above 9*scale,
	// so the digit reads directly as a multiple of the scale (at scale 10, "3" is 30-39ms).
	BarDigit
)

// barSets holds each Bar's CLI/config/legend name and its glyphs in ascending RTT
// order. The last glyph is the overflow; the ones before it are the bands. In linear
// mode band i covers [scale*i, scale*(i+1)); in log mode it covers the geometric band
// [floor*aⁱ, floor*a^(i+1)) with a = e^lnBase and floor = scale. Anything at or above
// the last band renders the overflow glyph in either mode. No glyph may collide with
// the failure glyphs (X/t/s/?), which IsFailGlyph tells apart by value.
var barSets = [...]struct {
	name   string
	glyphs []string
}{
	BarBlock: {"block", []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}},
	BarASCII: {"ascii", []string{"_", ".", "-", "=", "+", "*", "#", "@"}},
	BarDigit: {"digit", []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}},
}

// String returns the set's name as accepted by ParseBar ("block", "ascii", "digit").
func (b Bar) String() string {
	if b < 0 || int(b) >= len(barSets) {
		return barSets[BarBlock].name
	}

	return barSets[b].name
}

// Next returns the following set in the 'b'-key cycle, wrapping to the first.
func (b Bar) Next() Bar {
	if b < 0 || int(b) >= len(barSets) {
		return BarBlock
	}

	return (b + 1) % Bar(len(barSets))
}

// Chars returns every glyph of the set concatenated, so a caller can ask whether the
// terminal can display the whole set.
func (b Bar) Chars() string {
	return strings.Join(b.glyphs(), "")
}

// Levels returns how many levels the set draws: its bands plus the overflow. Level
// reports a success as an index in [0, Levels()).
func (b Bar) Levels() int {
	return len(b.glyphs())
}

// GlyphAt returns the set's glyph for a level from Level, clamping an out-of-range
// level to the nearest end so a stale index can never panic the render.
func (b Bar) GlyphAt(level int) string {
	g := b.glyphs()

	return g[min(max(level, 0), len(g)-1)]
}

// glyphs returns the set's glyphs, falling back to BarBlock for an out-of-range Bar so
// a value not built through ParseBar can never panic the render.
func (b Bar) glyphs() []string {
	if b < 0 || int(b) >= len(barSets) {
		return barSets[BarBlock].glyphs
	}

	return barSets[b].glyphs
}

// ParseBar returns the set named s (case-insensitive) and true, or BarBlock and false
// when s names no set.
func ParseBar(s string) (Bar, bool) {
	for i, set := range barSets {
		if strings.EqualFold(s, set.name) {
			return Bar(i), true
		}
	}

	return BarBlock, false
}

// BarNames lists the set names in cycle order, for the CLI usage text.
func BarNames() []string {
	names := make([]string, len(barSets))
	for i, set := range barSets {
		names[i] = set.name
	}

	return names
}

// boundaryEpsilon nudges the bucket index up before truncation so an RTT exactly on a
// band boundary — where the true step value is a whole number that float rounding can
// render a hair below it (rtt/scale for rtt=0.3, scale=0.1 is 2.9999999999999996, not
// 3.0) — lands in the band it opens rather than the one below. The trade-off is
// deliberate: an RTT within ~1e-9 in step space just under a boundary rounds up too, a
// sliver far below display resolution. levelForStep applies it, so every caller shares
// the nudge-then-truncate protocol without re-adding it.
const boundaryEpsilon = 1e-9

// NoLevel is Level's result for a failed probe, which has no place on the RTT scale.
const NoLevel = -1

// The failure glyphs. X is the one loss: a probe sent that got no reply (and a row that
// could not be built, which shows its reason beside it). The others are a probe that
// observed nothing about the target, which the statistics do not count: a relay timeout
// (t), a relay failure (s), and a probe this host could not send (?), such as one with no
// route, no socket or no address for its name. Each is one ASCII cell, and none is a
// glyph of a bar set, so a cell tells failure from success in every set.
const (
	FailureGlyph      = "X"
	relayTimeoutGlyph = "t"
	relayFailedGlyph  = "s"
	unavailableGlyph  = "?"
)

// Glyph maps a result to its result-bar character. Failures map to X/t/s/? (see the
// failure glyphs); a success maps to the glyph of bar at its Level (logarithmic when
// lnBase is positive, linear otherwise). The TUI calls this at render time, so the bar
// re-buckets when the scale, log factor or glyph set changes.
func Glyph(res probe.Result, scale, lnBase float64, bar Bar) string {
	switch res.Code {
	case probe.Success:
		return bar.GlyphAt(rttLevel(res.RTT, scale, lnBase, bar.Levels()))
	case probe.RelayTimeout:
		return relayTimeoutGlyph
	case probe.RelayFailed:
		return relayFailedGlyph
	case probe.Unavailable:
		return unavailableGlyph
	case probe.Failed:
		return FailureGlyph
	default:
		// A code this build does not know falls through to X below: a failure is X
		// unless it is known to have observed nothing.
	}

	return FailureGlyph
}

// Level returns where a result sits on the RTT scale: for a success, the index of the
// band Glyph draws it in (0 is the fastest band, bar.Levels()-1 the overflow); for a
// failure (any result Glyph draws as X/t/s/?, i.e. not probe.Result.IsSuccess), NoLevel.
// The TUI colors a cell by it, so the color and the glyph always agree on the band.
func Level(res probe.Result, scale, lnBase float64, bar Bar) int {
	if !res.IsSuccess() {
		return NoLevel
	}

	return rttLevel(res.RTT, scale, lnBase, bar.Levels())
}

// rttLevel picks the level of a successful probe's RTT on a bar of the given number of
// levels. The bucket index is computed in "step space" — rtt/scale for the linear
// scale (lnBase <= 0) or ln(rtt/scale)/lnBase for the logarithmic scale (a
// base-e^lnBase log) — then clamped by levelForStep. Both regimes share the half-open,
// left-closed band inclusion: band i covers [scale*i, scale*(i+1)) linearly,
// [scale*aⁱ, scale*a^(i+1)) logarithmically (a = e^lnBase). A degenerate scale or a
// non-positive RTT (where the ratio/log is undefined) falls back to the lowest level.
func rttLevel(rtt, scale, lnBase float64, levels int) int {
	if scale <= 0 || rtt <= 0 {
		return 0
	}

	step := rtt / scale
	if lnBase > 0 {
		step = math.Log(step) / lnBase
	}

	return levelForStep(step, levels)
}

// levelForStep maps a bucket index to a level in [0, levels), whose last level is the
// overflow: the bands are levels 0..n-1 with n = levels-1, and the index is clamped to
// [0, n]. A negative index (RTT below the first band) or NaN yields the lowest level,
// and an index at or above n overflows to level n. It applies boundaryEpsilon itself so
// the nudge-then-truncate protocol lives in one place rather than across the call
// boundary.
func levelForStep(step float64, levels int) int {
	bands := levels - 1

	step += boundaryEpsilon
	switch {
	case math.IsNaN(step) || step < 0:
		return 0
	case step >= float64(bands):
		return bands
	default:
		// step is in [0, bands) here, so the truncation is a valid band index.
		return int(step)
	}
}

// IsFailGlyph reports whether a glyph represents a failure (X/t/s/?).
func IsFailGlyph(g string) bool {
	switch g {
	case FailureGlyph, relayTimeoutGlyph, relayFailedGlyph, unavailableGlyph:
		return true
	default:
		return false
	}
}
