// Package monitor holds the per-target state and statistics that sit between the
// ping layer and the TUI: send counters, loss, RTT/average, the rolling result
// history, and the result-bar glyphs.
package monitor

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/yuu61/deadman/internal/ping"
)

// State is the reachability state of a target.
type State int

// Reachability states of a target.
const (
	Unknown State = iota
	Up
	Down
)

// percentMultiplier scales a 0..1 ratio to a 0..100 percentage.
const percentMultiplier = 100.0

// jitterGain is the RFC 3550 §6.4.1 smoothing divisor: Jit += (|ΔRTT| - Jit)/16.
// The 1/16 EWMA self-decays, so JIT reflects recent variation on this unbounded
// stream rather than freezing into a lifetime statistic.
const jitterGain = 16.0

// historyCap bounds the retained result history. We keep a fixed-size ring and
// slice it to the current terminal width at render time, so history survives a
// terminal resize rather than being capped at insert time by a width-dependent
// length.
const historyCap = 256

// Target tracks one ping destination and its running statistics.
type Target struct {
	Name   string
	Addr   string
	Source string
	TCP    string
	Relay  map[string]string
	Via    string // human label for the probing method (ping.Describe), shown in the VIA column.
	Pinger ping.Pinger

	State    State
	Loss     int
	LossRate float64
	RTT      float64 // current.
	Min      float64 // min successful RTT (lifetime).
	Max      float64 // max successful RTT (lifetime).
	Jit      float64 // RFC 3550 smoothed jitter (EWMA of |ΔRTT|), ms.
	Tot      float64 // sum of all successful RTTs.
	Avg      float64 // mean RTT.
	Snt      int     // number sent.
	TTL      int     // last TTL (captured, not displayed).

	history  []ping.Result // ring buffer of raw probe results; len == historyCap once seeded. newest is at histNext-1.
	histNext int           // ring index where the next result will be written.
	histLen  int           // number of valid results retained, capped at historyCap.
	prevRTT  float64       // previous successful RTT, for the jitter delta.

	// failed marks a placeholder built by NewFailedTarget (a target whose config could
	// not be constructed). It is excluded from reload history-reuse so that fixing the
	// config and reloading replaces the placeholder with the real target, rather than
	// the new target inheriting the always-fail placeholder via a matching Key().
	failed bool
}

// NewTarget builds a Target and its Pinger from a Spec.
func NewTarget(name string, spec ping.Spec) (*Target, error) {
	p, err := ping.New(spec)
	if err != nil {
		return nil, err
	}

	return &Target{
		Name:   name,
		Addr:   spec.Addr,
		Source: spec.Source,
		TCP:    spec.TCP,
		Relay:  spec.Relay,
		Via:    ping.Describe(spec),
		Pinger: p,
		State:  Unknown,
	}, nil
}

// NewFailedTarget builds a placeholder target that always reports a failure (a
// permanent X). buildRows uses it when a target's config cannot be constructed (e.g. a
// missing required relay attribute or an option-like operand) so that one target
// degrades visibly while monitoring of the rest continues, instead of aborting startup.
func NewFailedTarget(name, addr string) *Target {
	return &Target{
		Name:   name,
		Addr:   addr,
		Relay:  map[string]string{},
		Via:    "error",
		Pinger: ping.AlwaysFail(),
		State:  Unknown,
		failed: true,
	}
}

// IsFailed reports whether this is a NewFailedTarget placeholder. buildRows uses it to
// keep placeholders out of the reload history-reuse index.
func (t *Target) IsFailed() bool { return t.failed }

// Consume folds a probe result into the running statistics.
func (t *Target) Consume(res ping.Result) {
	t.Snt++
	if res.Success {
		t.State = Up
		t.RTT = res.RTT
		t.Tot += res.RTT
		t.Avg = t.Tot / float64(t.Snt)
		t.TTL = res.TTL
		t.foldSuccessRTT(res.RTT)
	} else {
		t.Loss++
		t.State = Down
	}

	t.LossRate = float64(t.Loss) / float64(t.Snt) * percentMultiplier

	if t.history == nil {
		t.history = make([]ping.Result, historyCap)
	}

	t.history[t.histNext] = res
	t.histNext = (t.histNext + 1) % historyCap

	if t.histLen < historyCap {
		t.histLen++
	}
}

// Len reports how many probe results are currently retained (at most historyCap).
func (t *Target) Len() int { return t.histLen }

// At returns the i-th most recent probe result: At(0) is the newest, At(Len()-1) the
// oldest still retained. The TUI renders each to a glyph at view time via Glyph, so the
// result bar re-buckets live when the RTT scale changes. Callers must keep 0 <= i < Len();
// the TUI bounds i with Len at render time.
func (t *Target) At(i int) ping.Result {
	idx := (t.histNext - 1 - i) % historyCap
	if idx < 0 {
		idx += historyCap
	}

	return t.history[idx]
}

// Refresh resets all statistics and history (the 'r' key).
func (t *Target) Refresh() {
	t.State = Unknown
	t.Loss = 0
	t.LossRate = 0
	t.RTT = 0
	t.Min = 0
	t.Max = 0
	t.Jit = 0
	t.Tot = 0
	t.Avg = 0
	t.Snt = 0
	t.TTL = 0
	t.history = nil
	t.histNext = 0
	t.histLen = 0
	t.prevRTT = 0
}

// Key is a stable identity used to preserve history across SIGHUP reloads. Relay
// keys are sorted so map iteration order cannot affect equality, and every field is
// strconv.Quote'd before concatenation so a value containing the ':'/'=' delimiters
// cannot forge a field boundary and alias another target's identity.
func (t *Target) Key() string {
	keys := make([]string, 0, len(t.Relay))
	for k := range t.Relay {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	var sb strings.Builder
	sb.WriteString(strconv.Quote(t.Name))
	sb.WriteString(":")
	sb.WriteString(strconv.Quote(t.Addr))

	for _, k := range keys {
		sb.WriteString(":")
		sb.WriteString(strconv.Quote(k))
		sb.WriteString("=")
		sb.WriteString(strconv.Quote(t.Relay[k]))
	}

	if t.Source != "" {
		sb.WriteString(":src=")
		sb.WriteString(strconv.Quote(t.Source))
	}

	if t.TCP != "" {
		sb.WriteString(":tcp=")
		sb.WriteString(strconv.Quote(t.TCP))
	}

	return sb.String()
}

// foldSuccessRTT folds a successful probe's RTT into the running min/max and the
// RFC 3550 smoothed jitter. It assumes t.Snt is already incremented, so Snt-Loss
// is the number of successes including this one; 1 means the first sample (seed
// min/max, no jitter delta yet). The success count is used rather than a
// prevRTT==0 sentinel so a genuine 0ms RTT is not mistaken for "no predecessor".
// Jitter is measured between consecutive successes; failures in between are
// skipped (matching mtr).
func (t *Target) foldSuccessRTT(rtt float64) {
	if t.Snt-t.Loss == 1 {
		t.Min = rtt
		t.Max = rtt
		t.prevRTT = rtt

		return
	}

	t.Min = min(t.Min, rtt)
	t.Max = max(t.Max, rtt)

	d := rtt - t.prevRTT
	if d < 0 {
		d = -d
	}

	t.Jit += (d - t.Jit) / jitterGain
	t.prevRTT = rtt
}

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
// the failure glyphs (X/t/s), which IsFailGlyph tells apart by value.
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

// Glyph maps a result to its result-bar character. Failures map to X/t/s; a
// success maps to the glyph of bar at its Level (logarithmic when lnBase is positive,
// linear otherwise). The TUI calls this at render time, so the bar re-buckets when the
// scale, log factor or glyph set changes.
func Glyph(res ping.Result, scale, lnBase float64, bar Bar) string {
	switch res.Code {
	case ping.SSHTimeout:
		return "t"
	case ping.SSHFailed:
		return "s"
	case ping.Success, ping.Failed:
		if !res.Success {
			return "X"
		}

		return bar.GlyphAt(rttLevel(res.RTT, scale, lnBase, bar.Levels()))
	default:
		// unknown code: treat as a plain failure.
		return "X"
	}
}

// Level returns where a result sits on the RTT scale: for a success, the index of the
// band Glyph draws it in (0 is the fastest band, bar.Levels()-1 the overflow); for a
// failure (any result Glyph draws as X/t/s), NoLevel. The TUI colors a cell by it, so
// the color and the glyph always agree on the band.
func Level(res ping.Result, scale, lnBase float64, bar Bar) int {
	if !res.Success || (res.Code != ping.Success && res.Code != ping.Failed) {
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

// IsFailGlyph reports whether a glyph represents a failure (X/t/s).
func IsFailGlyph(g string) bool {
	return g == "X" || g == "t" || g == "s"
}
