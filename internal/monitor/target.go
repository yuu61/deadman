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

	history []ping.Result // raw probe results (newest first); rendered to glyphs at view time.
	prevRTT float64       // previous successful RTT, for the jitter delta.

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

	t.history = append([]ping.Result{res}, t.history...)
	if len(t.history) > historyCap {
		t.history = t.history[:historyCap]
	}
}

// Results returns the most recent n probe results (newest first). The TUI renders
// each to a glyph at view time via Glyph, so the result bar re-buckets live when
// the RTT scale changes.
func (t *Target) Results(n int) []ping.Result {
	if n > len(t.history) {
		n = len(t.history)
	}

	if n < 0 {
		n = 0
	}

	return t.history[:n]
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

// rttBars are the block elements for ascending RTT buckets. In linear mode bar i
// covers [scale*i, scale*(i+1)); in log mode it covers the geometric band
// [floor*aⁱ, floor*a^(i+1)) with a = e^logK and floor = scale. The full block "█"
// renders for anything at or above the last band in either mode.
var rttBars = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇"}

// boundaryEpsilon nudges the log segment index up before truncation so a value
// landing exactly on a band boundary (floor*aⁱ) is not pushed into the band below
// by floating-point error (e.g. floor=0.3, rtt=floor*e² yields 1.9999999998 rather
// than 2.0). True band boundaries are a full 1.0 apart in index space, so this can
// never promote a genuine non-boundary value into the next band.
const boundaryEpsilon = 1e-9

// Glyph maps a result to its result-bar character. Failures map to X/t/s; a
// success maps to a block element bucketed by rttGlyph (linear when logK is 0,
// logarithmic otherwise). The TUI calls this at render time, so the bar re-buckets
// when the scale or log factor changes.
func Glyph(res ping.Result, scale float64, logK int) string {
	switch res.Code {
	case ping.SSHTimeout:
		return "t"
	case ping.SSHFailed:
		return "s"
	case ping.Success, ping.Failed:
		if !res.Success {
			return "X"
		}

		return rttGlyph(res.RTT, scale, logK)
	default:
		// unknown code: treat as a plain failure.
		return "X"
	}
}

// rttGlyph picks the block element for a successful probe's RTT. With logK == 0 it
// buckets linearly: bar i covers [scale*i, scale*(i+1)), and "█" sits above the
// last bucket. With logK > 0 it delegates to logIndexGlyph, which buckets on a
// logarithmic window whose floor is scale and whose factor is e^logK.
func rttGlyph(rtt, scale float64, logK int) string {
	if logK > 0 {
		return logIndexGlyph(rtt, scale, logK)
	}

	for i, bar := range rttBars {
		if rtt < scale*float64(i+1) {
			return bar
		}
	}

	return "█"
}

// logIndexGlyph buckets rtt on a logarithmic scale: the segment index is
// floor(ln(rtt/floor)/logK) = log_{e^logK}(rtt/floor), clamped to [0, len(rttBars)].
// Band i covers [floor*aⁱ, floor*a^(i+1)) with a = e^logK, mirroring the linear
// half-open, left-closed inclusion; an index at or above len(rttBars) overflows to
// "█" and a sub-floor RTT clamps to the lowest bar. A degenerate floor or a
// non-positive RTT (where ln is undefined) also falls back to the lowest bar.
func logIndexGlyph(rtt, floor float64, logK int) string {
	if floor <= 0 || rtt <= 0 {
		return rttBars[0]
	}

	raw := math.Log(rtt/floor)/float64(logK) + boundaryEpsilon

	switch {
	case math.IsNaN(raw) || raw < 0:
		return rttBars[0]
	case raw >= float64(len(rttBars)):
		return "█"
	default:
		// raw is in [0, len(rttBars)) here, so the truncation is a valid bar index.
		return rttBars[int(raw)]
	}
}

// IsFailGlyph reports whether a glyph represents a failure (rendered in red).
func IsFailGlyph(g string) bool {
	return g == "X" || g == "t" || g == "s"
}
