package palette

import (
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// parseHex reads a "#RRGGBB" TrueColor value back into an srgb.
func parseHex(t *testing.T, s string) srgb {
	t.Helper()

	if len(s) != len("#RRGGBB") || s[0] != '#' {
		t.Fatalf("%q is not #RRGGBB", s)
	}

	var c srgb

	for i := range c {
		v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}

		c[i] = int(v)
	}

	return c
}

// parse256 reads an ANSI256 value back into its xterm-256 color, failing on an index
// outside the 16–255 range the ramp may use.
func parse256(t *testing.T, s string) srgb {
	t.Helper()

	i, err := strconv.Atoi(s)
	if err != nil || i < cubeFirst || i > grayLast {
		t.Fatalf("ANSI256 %q is not an index in [16, 255]", s)
	}

	return xterm256(i)
}

// TestRampReference pins the 8-level ramp (the block and ascii bars) at every depth and
// background. The 24-bit and 256-color values were computed independently (a Python
// Oklab/WCAG prototype), so this cross-checks the Go math as well as guarding the
// palette against drift.
func TestRampReference(t *testing.T) {
	want := []struct {
		dark, dark256, ansiDark, light, light256, ansiLight string
	}{
		{"#03AF7A", "36", "10", "#02A976", "65", "2"},
		{"#72C36F", "78", "10", "#5FA45C", "65", "2"},
		{"#AED65C", "149", "11", "#809F42", "100", "3"},
		{"#E5E838", "184", "11", "#989A21", "136", "3"},
		{"#FFE100", "220", "11", "#A99500", "136", "3"},
		{"#FFC200", "214", "11", "#BB8D00", "136", "3"},
		{"#FFA200", "214", "3", "#D08300", "136", "3"},
		{"#FF8000", "208", "3", "#E97400", "202", "3"},
	}

	got := Ramp(len(want))
	if len(got) != len(want) {
		t.Fatalf("Ramp(%d) has %d levels", len(want), len(got))
	}

	for i, w := range want {
		wantColor := lipgloss.CompleteAdaptiveColor{
			Dark: lipgloss.CompleteColor{TrueColor: w.dark, ANSI256: w.dark256, ANSI: w.ansiDark},
			Light: lipgloss.CompleteColor{
				TrueColor: w.light,
				ANSI256:   w.light256,
				ANSI:      w.ansiLight,
			},
		}
		if got[i] != wantColor {
			t.Errorf("level %d = %+v, want %+v", i, got[i], wantColor)
		}
	}
}

// TestRampAnchors checks the stops land exactly on the anchor colors: level 0 is the
// safe green and the last level the warning orange at any size, and an odd-sized ramp
// puts the caution yellow in the middle.
func TestRampAnchors(t *testing.T) {
	for _, n := range []int{2, 3, 5, 8, 10} {
		r := Ramp(n)
		if got := r[0].Dark.TrueColor; got != "#03AF7A" {
			t.Errorf("Ramp(%d)[0] = %s, want the safe green #03AF7A", n, got)
		}

		if got := r[n-1].Dark.TrueColor; got != "#FF8000" {
			t.Errorf("Ramp(%d)[%d] = %s, want the warning orange #FF8000", n, n-1, got)
		}

		if n%2 == 1 {
			if got := r[n/2].Dark.TrueColor; got != "#FFF100" {
				t.Errorf("Ramp(%d)[%d] = %s, want the caution yellow #FFF100", n, n/2, got)
			}
		}
	}
}

// TestSeenByProtan checks the reddest colors, the ramp's warning orange and the failure
// red, keep the 3:1 non-text contrast against black for protan vision too, in 24-bit
// and 256-color form. Protan vision loses the long wavelengths, so these are the colors
// that darken most there. Protan vision is simulated with Machado, Oliveira and
// Fernandes (2009) at severity 1, a matrix on linear sRGB.
func TestSeenByProtan(t *testing.T) {
	protan := [3]vec{
		{0.152286, 1.052583, -0.204868},
		{0.114503, 0.786281, 0.099216},
		{-0.003882, -0.048116, 1.051998},
	}

	cases := []struct {
		name string
		col  lipgloss.CompleteColor
	}{
		{"warning orange", Ramp(len(anchors))[len(anchors)-1].Dark},
		{"failure red", Failure().Dark},
	}
	for _, k := range cases {
		for _, c := range []srgb{parseHex(t, k.col.TrueColor), parse256(t, k.col.ANSI256)} {
			y := luminance(mul(&protan, c.linear()))
			if got := (y + flare) / flare; got < minContrast {
				t.Errorf("%s %s seen by protan: contrast %.3f < %.1f on black",
					k.name, c.hex(), got, minContrast)
			}
		}
	}
}

// TestRampReadable checks every level at every size meets the WCAG 2 non-text contrast
// of 3:1 on its background: against black for the dark variant, against white for the
// light one, in both 24-bit and 256-color form.
func TestRampReadable(t *testing.T) {
	contrast := func(y1, y2 float64) float64 {
		return (max(y1, y2) + flare) / (min(y1, y2) + flare)
	}

	for n := 1; n <= 12; n++ {
		for i, c := range Ramp(n) {
			checks := []struct {
				name string
				c    srgb
				bg   float64
			}{
				{"dark", parseHex(t, c.Dark.TrueColor), 0},
				{"dark256", parse256(t, c.Dark.ANSI256), 0},
				{"light", parseHex(t, c.Light.TrueColor), 1},
				{"light256", parse256(t, c.Light.ANSI256), 1},
			}
			for _, k := range checks {
				// A hair of tolerance: the caps are exact in real arithmetic, and the
				// darkening floors its channels to stay on the right side of them.
				if got := contrast(luminance(k.c.linear()), k.bg); got < minContrast-1e-9 {
					t.Errorf(
						"Ramp(%d)[%d] %s %s: contrast %.3f < %.1f",
						n,
						i,
						k.name,
						k.c.hex(),
						got,
						minContrast,
					)
				}
			}
		}
	}
}

// TestRampHueHeadsToWarning checks the ramp moves one way, green → yellow → orange, with
// no hue doubling back: the Oklab hue angle strictly decreases level by level.
func TestRampHueHeadsToWarning(t *testing.T) {
	for _, n := range []int{8, 10} {
		prev := math.Inf(1)

		for i, c := range Ramp(n) {
			lab := parseHex(t, c.Dark.TrueColor).oklab()

			h := math.Atan2(lab[2], lab[1])
			if h >= prev {
				t.Errorf(
					"Ramp(%d)[%d] %s: hue %.3f rad does not decrease from %.3f",
					n,
					i,
					c.Dark.TrueColor,
					h,
					prev,
				)
			}

			prev = h
		}
	}
}

// TestRampANSIZones checks the 16-color fallback splits the levels among the three
// anchors by nearness — green for the fast quarter, the orange's stand-in for the slow
// quarter, yellow between. On a dark background that is the bright green / yellow and
// the normal yellow; on a light one the normal green / yellow, the yellow covering the
// slow quarter too. No level falls back to red.
func TestRampANSIZones(t *testing.T) {
	cases := []struct {
		n           int
		dark, light []string
	}{
		{8, zones(2, 4, 2, "10", "11"), zones(2, 4, 2, "2", "3")},
		{10, zones(3, 4, 3, "10", "11"), zones(3, 4, 3, "2", "3")},
		{3, zones(1, 1, 1, "10", "11"), zones(1, 1, 1, "2", "3")},
	}
	for _, c := range cases {
		var dark, light []string

		for _, col := range Ramp(c.n) {
			dark = append(dark, col.Dark.ANSI)
			light = append(light, col.Light.ANSI)
		}

		if !slices.Equal(dark, c.dark) || !slices.Equal(light, c.light) {
			t.Errorf(
				"Ramp(%d) ANSI dark %v light %v, want dark %v light %v",
				c.n,
				dark,
				light,
				c.dark,
				c.light,
			)
		}
	}
}

// zones spells out a zone split: g copies of green, y of yellow and o of the orange's
// stand-in, the normal yellow (3) on either background.
func zones(g, y, o int, green, yellow string) []string {
	out := slices.Repeat([]string{green}, g)
	out = append(out, slices.Repeat([]string{yellow}, y)...)

	return append(out, slices.Repeat([]string{"3"}, o)...)
}

func TestRampDegenerateSizes(t *testing.T) {
	for _, n := range []int{-1, 0} {
		if got := Ramp(n); len(got) != 0 {
			t.Errorf("Ramp(%d) = %v, want empty", n, got)
		}
	}

	if got := Ramp(1); len(got) != 1 || got[0].Dark.TrueColor != "#03AF7A" {
		t.Errorf("Ramp(1) = %+v, want the single safe green", got)
	}
}

// TestOklabRoundTrip checks the Oklab matrices invert each other: every anchor and
// every xterm-256 entry survives sRGB → Oklab → sRGB unchanged.
func TestOklabRoundTrip(t *testing.T) {
	colors := make([]srgb, 0, len(anchors)+grayLast-cubeFirst+1)
	for _, a := range anchors {
		colors = append(colors, a.color)
	}

	for i := cubeFirst; i <= grayLast; i++ {
		colors = append(colors, xterm256(i))
	}

	for _, c := range colors {
		if got := fromLinear(oklabToLinear(c.oklab()), math.Round); got != c {
			t.Errorf("round trip %s -> %s", c.hex(), got.hex())
		}
	}
}

func TestXterm256(t *testing.T) {
	cases := map[int]srgb{
		16:  {0, 0, 0},
		21:  {0, 0, 0xFF},
		196: {0xFF, 0, 0},
		202: {0xFF, 0x5F, 0},
		231: {0xFF, 0xFF, 0xFF},
		232: {8, 8, 8},
		255: {238, 238, 238},
	}
	for i, want := range cases {
		if got := xterm256(i); got != want {
			t.Errorf("xterm256(%d) = %s, want %s", i, got.hex(), want.hex())
		}
	}
}

// TestFailure pins the failure red: CUD ver.3's #FF2800 and xterm 196 on either
// background, as both already reach 3:1 against black and white, and at 16 colors the
// bright red on a dark background and the normal red on a light one.
func TestFailure(t *testing.T) {
	want := lipgloss.CompleteAdaptiveColor{
		Dark:  lipgloss.CompleteColor{TrueColor: "#FF2800", ANSI256: "196", ANSI: "9"},
		Light: lipgloss.CompleteColor{TrueColor: "#FF2800", ANSI256: "196", ANSI: "1"},
	}
	if got := Failure(); got != want {
		t.Errorf("Failure() = %+v, want %+v", got, want)
	}
}

// TestFailureApartFromRamp checks a failure can never share a color with a ramp level,
// at any color depth on either background: no level uses the red.
func TestFailureApartFromRamp(t *testing.T) {
	f := Failure()

	for _, n := range []int{8, 10} {
		for i, c := range Ramp(n) {
			for _, p := range [][2]lipgloss.CompleteColor{{c.Dark, f.Dark}, {c.Light, f.Light}} {
				level, fail := p[0], p[1]
				if level.TrueColor == fail.TrueColor || level.ANSI256 == fail.ANSI256 ||
					level.ANSI == fail.ANSI {
					t.Errorf("Ramp(%d)[%d] %+v collides with Failure %+v", n, i, level, fail)
				}
			}
		}
	}
}
