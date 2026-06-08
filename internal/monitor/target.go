// Package monitor holds the per-target state and statistics that sit between the
// ping layer and the TUI: send counters, loss, RTT/average, the rolling result
// history, and the result-bar glyphs.
package monitor

import (
	"slices"
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

	history []string
	scale   int
	prevRTT float64 // previous successful RTT, for the jitter delta.
}

// NewTarget builds a Target and its Pinger from a Spec.
func NewTarget(name string, spec ping.Spec, scale int) (*Target, error) {
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
		Pinger: p,
		State:  Unknown,
		scale:  scale,
	}, nil
}

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

	t.history = append([]string{glyph(res, t.scale)}, t.history...)
	if len(t.history) > historyCap {
		t.history = t.history[:historyCap]
	}
}

// Glyphs returns the most recent n result glyphs (newest first).
func (t *Target) Glyphs(n int) []string {
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
// keys are sorted so map iteration order cannot affect equality.
func (t *Target) Key() string {
	keys := make([]string, 0, len(t.Relay))
	for k := range t.Relay {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	var sb strings.Builder
	sb.WriteString(t.Name)
	sb.WriteString(":")
	sb.WriteString(t.Addr)

	for _, k := range keys {
		sb.WriteString(":")
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(t.Relay[k])
	}

	if t.Source != "" {
		sb.WriteString(":src=")
		sb.WriteString(t.Source)
	}

	if t.TCP != "" {
		sb.WriteString(":tcp=")
		sb.WriteString(t.TCP)
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

// rttBars are the block elements for ascending RTT buckets; bar i is used when
// RTT < scale*(i+1), and the full block "█" for anything above the last bucket.
var rttBars = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇"}

// glyph maps a result to its result-bar character. Failures map to X/t/s; a
// success maps to a block element scaled by the RTT scale (ms per step).
func glyph(res ping.Result, scale int) string {
	switch res.Code {
	case ping.SSHTimeout:
		return "t"
	case ping.SSHFailed:
		return "s"
	case ping.Success, ping.Failed:
		if !res.Success {
			return "X"
		}

		return rttGlyph(res.RTT, scale)
	default:
		// unknown code: treat as a plain failure.
		return "X"
	}
}

// rttGlyph picks the block element for a successful probe's RTT.
func rttGlyph(rtt float64, scale int) string {
	step := float64(scale)
	for i, bar := range rttBars {
		if rtt < step*float64(i+1) {
			return bar
		}
	}

	return "█"
}

// IsFailGlyph reports whether a glyph represents a failure (rendered in red).
func IsFailGlyph(g string) bool {
	return g == "X" || g == "t" || g == "s"
}
