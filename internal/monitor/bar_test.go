package monitor

import (
	"math"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/yuu61/deadman/internal/ping"
)

// okRTT builds a successful probe result with the given RTT.
func okRTT(rtt float64) ping.Result {
	return ping.Result{Success: true, Code: ping.Success, RTT: rtt}
}

// TestBarSetsWellFormed pins the invariants every glyph set relies on: at least one band
// plus the overflow, one rune per glyph (so each is one result-bar cell), no duplicate
// glyph (a repeat would make two buckets indistinguishable), and no collision with the
// failure glyphs IsFailGlyph picks out. The ASCII and digit sets must stay pure ASCII,
// which is the whole point of offering them on fonts without block elements.
func TestBarSetsWellFormed(t *testing.T) {
	for _, bar := range []Bar{BarBlock, BarASCII, BarDigit} {
		g := bar.glyphs()
		if len(g) < 2 {
			t.Fatalf("%s: %d glyphs, want at least one band plus the overflow", bar, len(g))
		}

		seen := map[string]bool{}

		for _, ch := range g {
			if utf8.RuneCountInString(ch) != 1 {
				t.Errorf("%s: glyph %q is not a single rune", bar, ch)
			}

			if seen[ch] {
				t.Errorf("%s: glyph %q repeats", bar, ch)
			}

			seen[ch] = true

			if IsFailGlyph(ch) {
				t.Errorf("%s: glyph %q collides with a failure glyph", bar, ch)
			}

			if bar != BarBlock && ch[0] >= utf8.RuneSelf {
				t.Errorf("%s: glyph %q is not ASCII", bar, ch)
			}
		}
	}

	if got := BarBlock.Chars(); got != "▁▂▃▄▅▆▇█" {
		t.Errorf("BarBlock.Chars() = %q, want ▁▂▃▄▅▆▇█", got)
	}
}

// TestBarASCIIMatchesBlockThresholds guards the ASCII fallback's contract: it only
// respells the block bar, so every RTT lands in the same bucket index under both sets,
// in linear and log mode alike. A drift here would make the auto-fallback change what
// the operator's -s/scale means.
func TestBarASCIIMatchesBlockThresholds(t *testing.T) {
	block, ascii := BarBlock.glyphs(), BarASCII.glyphs()
	if len(block) != len(ascii) {
		t.Fatalf("len(ascii) = %d, want len(block) = %d", len(ascii), len(block))
	}

	for _, lnBase := range []float64{0, 1, 2} {
		for rtt := 0.0; rtt <= 2000; rtt += 0.5 {
			res := okRTT(rtt)
			bi := slices.Index(block, Glyph(res, 10, lnBase, BarBlock))
			ai := slices.Index(ascii, Glyph(res, 10, lnBase, BarASCII))

			if bi != ai {
				t.Fatalf("rtt=%v lnBase=%g: block bucket %d, ascii bucket %d", rtt, lnBase, bi, ai)
			}
		}
	}
}

// TestGlyphDigit pins the 0-9 set's reading: digit d covers [scale*d, scale*(d+1)) and 9
// is everything at or above 9*scale, so at scale 10 the digit is the tens of ms. The
// boundary cases reuse boundaryEpsilon's float guard (0.1*3 is 0.30000000000000004).
func TestGlyphDigit(t *testing.T) {
	cases := []struct {
		name       string
		res        ping.Result
		scale      float64
		lnBase     float64
		want       string
		wantReason string
	}{
		{"sub_scale", okRTT(9.9), 10, 0, "0", "below the first boundary"},
		{"first_band", okRTT(10), 10, 0, "1", "a boundary opens its band"},
		{"mid", okRTT(35), 10, 0, "3", "30-39ms reads as 3"},
		{"last_band", okRTT(89.9), 10, 0, "8", "just below the overflow"},
		{"overflow_edge", okRTT(90), 10, 0, "9", "9*scale overflows"},
		{"overflow_far", okRTT(5000), 10, 0, "9", "far overflow clamps to 9"},
		{"ms_scale", okRTT(7.2), 1, 0, "7", "at scale 1 the digit is whole ms"},
		{"float_boundary", okRTT(0.3), 0.1, 0, "3", "boundaryEpsilon float guard"},
		{"zero_rtt", okRTT(0), 10, 0, "0", "degenerate RTT falls to the lowest glyph"},
		{"log_band_8", okRTT(math.Exp(8)), 1, 1, "8", "log band 8 at floor*e^8"},
		{"log_overflow", okRTT(math.Exp(9)), 1, 1, "9", "log overflow at floor*e^9"},
		{"failed", ping.Result{Code: ping.Failed}, 10, 0, "X", "failures keep X"},
		{"ssh_timeout", ping.Result{Code: ping.SSHTimeout}, 10, 0, "t", "failures keep t"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Glyph(c.res, c.scale, c.lnBase, BarDigit); got != c.want {
				t.Errorf("Glyph(%+v, scale %v, lnBase %g, digit) = %q, want %q (%s)",
					c.res, c.scale, c.lnBase, got, c.want, c.wantReason)
			}
		})
	}
}

func TestParseBar(t *testing.T) {
	cases := []struct {
		in   string
		want Bar
		ok   bool
	}{
		{"block", BarBlock, true},
		{"ascii", BarASCII, true},
		{"digit", BarDigit, true},
		{"DIGIT", BarDigit, true}, // case-insensitive, like the directive keywords.
		{"auto", BarBlock, false}, // auto is a CLI/config policy, not a glyph set.
		{"", BarBlock, false},
		{"digits", BarBlock, false},
	}
	for _, c := range cases {
		got, ok := ParseBar(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseBar(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}

	// Every name round-trips, and BarNames lists them in cycle order.
	for i, name := range BarNames() {
		b, ok := ParseBar(name)
		if !ok || b != Bar(i) || b.String() != name {
			t.Errorf("BarNames()[%d] = %q does not round-trip (got %v, %v)", i, name, b, ok)
		}
	}
}

func TestBarNextCycles(t *testing.T) {
	want := []Bar{BarASCII, BarDigit, BarBlock}

	b := BarBlock
	for i, w := range want {
		b = b.Next()
		if b != w {
			t.Fatalf("step %d: Next() = %v, want %v", i+1, b, w)
		}
	}
}

// TestBarOutOfRangeFallsBackToBlock guards the defensive clamp: a Bar not built through
// ParseBar (e.g. a zero-initialized or corrupted value) must render the block set rather
// than panic on an index out of range.
func TestBarOutOfRangeFallsBackToBlock(t *testing.T) {
	for _, b := range []Bar{-1, 3, 99} {
		if got := Glyph(okRTT(35), 10, 0, b); got != "▄" {
			t.Errorf("Glyph(RTT 35, Bar(%d)) = %q, want the block glyph ▄", int(b), got)
		}

		if got := b.String(); got != "block" {
			t.Errorf("Bar(%d).String() = %q, want block", int(b), got)
		}

		if got := b.Next(); got != BarBlock {
			t.Errorf("Bar(%d).Next() = %v, want block", int(b), got)
		}
	}
}

// TestLevelMatchesGlyph guards the contract the TUI's coloring relies on: Level names the
// very band Glyph draws, for every set in linear and log mode, and NoLevel exactly when
// Glyph draws a failure — so a cell's color can never disagree with its glyph.
func TestLevelMatchesGlyph(t *testing.T) {
	results := []ping.Result{
		{Code: ping.Failed},
		{Code: ping.SSHTimeout},
		{Code: ping.SSHFailed},
		{
			Success: true,
			Code:    ping.SSHTimeout,
		}, // a relay failure is a failure regardless of Success.
		{Success: true, Code: ping.ResultCode(99)},
	}
	for rtt := 0.0; rtt <= 2000; rtt += 0.5 {
		results = append(results, okRTT(rtt))
	}

	for _, bar := range []Bar{BarBlock, BarASCII, BarDigit} {
		for _, lnBase := range []float64{0, 1, 2} {
			for _, res := range results {
				g := Glyph(res, 10, lnBase, bar)
				lv := Level(res, 10, lnBase, bar)

				switch {
				case IsFailGlyph(g):
					if lv != NoLevel {
						t.Errorf(
							"%s lnBase=%g %+v: Glyph %q is a failure but Level = %d",
							bar,
							lnBase,
							res,
							g,
							lv,
						)
					}
				case lv < 0 || lv >= bar.Levels():
					t.Errorf(
						"%s lnBase=%g %+v: Level = %d outside [0, %d)",
						bar,
						lnBase,
						res,
						lv,
						bar.Levels(),
					)
				case bar.GlyphAt(lv) != g:
					t.Errorf("%s lnBase=%g %+v: GlyphAt(Level %d) = %q, Glyph = %q",
						bar, lnBase, res, lv, bar.GlyphAt(lv), g)
				default:
					// Level names the band Glyph drew.
				}
			}
		}
	}
}

func TestBarLevels(t *testing.T) {
	for bar, want := range map[Bar]int{BarBlock: 8, BarASCII: 8, BarDigit: 10} {
		if got := bar.Levels(); got != want {
			t.Errorf("%s.Levels() = %d, want %d", bar, got, want)
		}
	}

	// GlyphAt clamps rather than panicking on a stale or corrupted level.
	if got := BarDigit.GlyphAt(-1); got != "0" {
		t.Errorf("GlyphAt(-1) = %q, want the lowest glyph 0", got)
	}

	if got := BarDigit.GlyphAt(99); got != "9" {
		t.Errorf("GlyphAt(99) = %q, want the overflow glyph 9", got)
	}
}
