// Package monitor holds the per-target state and statistics that sit between the
// ping layer and the TUI: send counters, loss, RTT/average, the rolling result
// history, and the result-bar glyphs.
package monitor

import (
	"sort"
	"strings"

	"github.com/yuu61/deadman/internal/ping"
)

// State is the reachability state of a target.
type State int

const (
	Unknown State = iota
	Up
	Down
)

// historyCap bounds the retained result history. Unlike the original (which
// capped on insert using the terminal-width-dependent length, losing history on
// shrink/grow), we keep a fixed ring and slice to the current width at render.
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
	RTT      float64 // current
	Tot      float64 // sum of all successful RTTs
	Avg      float64 // mean RTT
	Snt      int     // number sent
	TTL      int     // last TTL (captured, not displayed)

	history []string
	scale   int
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
	} else {
		t.Loss++
		t.State = Down
	}
	t.LossRate = float64(t.Loss) / float64(t.Snt) * 100.0

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
	t.Tot = 0
	t.Avg = 0
	t.Snt = 0
	t.TTL = 0
	t.history = nil
}

// Key is a stable identity used to preserve history across SIGHUP reloads. It is
// the Go equivalent of the original __str__/__eq__ but with relay keys sorted so
// map iteration order cannot affect equality.
func (t *Target) Key() string {
	keys := make([]string, 0, len(t.Relay))
	for k := range t.Relay {
		keys = append(keys, k)
	}
	sort.Strings(keys)

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

// glyph maps a result to its result-bar character. Failures map to X/t/s; a
// success maps to a block element scaled by the RTT scale (ms per step).
func glyph(res ping.Result, scale int) string {
	switch res.Code {
	case ping.SSHTimeout:
		return "t"
	case ping.SSHFailed:
		return "s"
	}
	if !res.Success {
		return "X"
	}
	s := float64(scale)
	switch {
	case res.RTT < s*1:
		return "▁"
	case res.RTT < s*2:
		return "▂"
	case res.RTT < s*3:
		return "▃"
	case res.RTT < s*4:
		return "▄"
	case res.RTT < s*5:
		return "▅"
	case res.RTT < s*6:
		return "▆"
	case res.RTT < s*7:
		return "▇"
	default:
		return "█"
	}
}

// IsFailGlyph reports whether a glyph represents a failure (rendered in red).
func IsFailGlyph(g string) bool {
	return g == "X" || g == "t" || g == "s"
}
