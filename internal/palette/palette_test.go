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

// contrast is the WCAG 2 contrast ratio between two relative luminances.
func contrast(y1, y2 float64) float64 {
	return (max(y1, y2) + flare) / (min(y1, y2) + flare)
}

// TestRampReference pins the 8-level ramp (the block and ascii bars) at every depth and
// background. The 24-bit and 256-color values were computed independently (a Python
// Oklab/WCAG prototype), so this cross-checks the Go math as well as guarding the
// palette against drift.
func TestRampReference(t *testing.T) {
	want := []struct {
		dark, dark256, ansiDark, light, light256, ansiLight string
	}{
		{"#00AAAA", "37", "6", "#00A5A5", "66", "6"},
		{"#68BE99", "72", "6", "#559E7F", "65", "6"},
		{"#9FD083", "150", "11", "#72965D", "101", "3"},
		{"#D0E161", "185", "11", "#838E3A", "100", "3"},
		{"#FFF000", "226", "11", "#8E8600", "100", "3"},
		{"#FFCC00", "220", "11", "#9B7B00", "100", "3"},
		{"#FFA700", "214", "3", "#AA6E00", "130", "3"},
		{"#FF8000", "208", "3", "#BB5C00", "130", "3"},
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

// TestRampAnchors checks the ends land exactly on the anchor colors, level 0 on the
// safe cyan and the last level on the warning orange at any size, and that the path
// runs through the caution yellow: a fine ramp has a level within 0.01 of it in Oklab.
// (Spacing by distance puts the yellow where the path's length says, not at the middle
// level.)
func TestRampAnchors(t *testing.T) {
	for _, n := range []int{2, 3, 5, 8, 10} {
		r := Ramp(n)
		if got := r[0].Dark.TrueColor; got != "#00AAAA" {
			t.Errorf("Ramp(%d)[0] = %s, want the safe cyan #00AAAA", n, got)
		}

		if got := r[n-1].Dark.TrueColor; got != "#FF8000" {
			t.Errorf("Ramp(%d)[%d] = %s, want the warning orange #FF8000", n, n-1, got)
		}
	}

	const fine, near = 101, 0.01

	yellow := anchors[1].color.oklab()

	closest := math.Inf(1)
	for _, c := range Ramp(fine) {
		closest = min(closest, math.Sqrt(distSq(parseHex(t, c.Dark.TrueColor).oklab(), yellow)))
	}

	if closest > near {
		t.Errorf("Ramp(%d) comes no closer than %.3f to the caution yellow", fine, closest)
	}
}

// TestRampEvenSteps checks the levels are spaced by distance along the path: for the bar
// sizes (8 and 10 levels), the smallest step between neighbors is at least 3/4 of the
// largest. The one short step straddles the caution yellow, where the path turns and
// the orange segment clips at the edge of sRGB; spacing by the anchors instead left two
// near-identical levels there (a ratio of 0.4).
func TestRampEvenSteps(t *testing.T) {
	const minRatio = 0.75

	for _, n := range []int{8, 10} {
		r := Ramp(n)

		lo, hi := math.Inf(1), 0.0

		for i := 1; i < n; i++ {
			d := math.Sqrt(distSq(
				parseHex(t, r[i-1].Dark.TrueColor).oklab(),
				parseHex(t, r[i].Dark.TrueColor).oklab(),
			))
			lo, hi = min(lo, d), max(hi, d)
		}

		if lo < minRatio*hi {
			t.Errorf("Ramp(%d) steps range %.4f..%.4f, want the smallest >= %.2f of the largest",
				n, lo, hi, minRatio)
		}
	}
}

// TestRampDarkensOnLight checks the light variant reads darker level by level: its
// luminance strictly falls, from the 3:1 non-text contrast against white at the fastest
// level to the 4.5:1 text contrast at the slowest.
func TestRampDarkensOnLight(t *testing.T) {
	for n := 2; n <= 12; n++ {
		r := Ramp(n)

		prev := math.Inf(1)

		for i, c := range r {
			y := luminance(parseHex(t, c.Light.TrueColor).linear())
			if y >= prev {
				t.Errorf("Ramp(%d)[%d] light %s: luminance %.4f does not fall from %.4f",
					n, i, c.Light.TrueColor, y, prev)
			}

			prev = y
		}

		last := luminance(parseHex(t, r[n-1].Light.TrueColor).linear())
		if got := contrast(last, 1); got < minTextContrast {
			t.Errorf("Ramp(%d) slowest light level: contrast %.3f < %.1f", n, got, minTextContrast)
		}
	}
}

// TestSeenByProtan checks the reddest level, the warning orange, keeps the 3:1 non-text
// contrast against black for protan vision too, in 24-bit and 256-color form. Protan
// vision loses the long wavelengths, so it is the level that darkens most there.
func TestSeenByProtan(t *testing.T) {
	col := Ramp(len(anchors))[len(anchors)-1].Dark
	for _, c := range []srgb{parseHex(t, col.TrueColor), parse256(t, col.ANSI256)} {
		y := luminance(mul(&protanSim, c.linear()))
		if got := contrast(y, 0); got < minContrast {
			t.Errorf("warning orange %s seen by protan: contrast %.3f < %.1f on black",
				c.hex(), got, minContrast)
		}
	}
}

// TestRampReadable checks every level at every size meets the WCAG 2 non-text contrast
// of 3:1 on its background: against black for the dark variant, against white for the
// light one, in both 24-bit and 256-color form.
func TestRampReadable(t *testing.T) {
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

// TestRampHueHeadsToWarning checks the ramp moves one way, cyan → yellow → orange, with no
// hue doubling back: the Oklab hue angle, taken in [0, 2π) since the cyan sits past π,
// strictly decreases level by level.
func TestRampHueHeadsToWarning(t *testing.T) {
	for _, n := range []int{8, 10} {
		prev := math.Inf(1)

		for i, c := range Ramp(n) {
			lab := parseHex(t, c.Dark.TrueColor).oklab()

			h := math.Mod(math.Atan2(lab[2], lab[1])+2*math.Pi, 2*math.Pi)
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
// anchors by nearness — the green's stand-in for the fast quarter, the orange's for the
// slow quarter, the yellow's between. The green's is the cyan on either background; the
// yellow's is the bright yellow on a dark background and the normal one on a light one,
// where it covers the slow quarter too. No level falls back to red.
func TestRampANSIZones(t *testing.T) {
	cases := []struct {
		n           int
		dark, light []string
	}{
		{8, zones(2, 4, 2, "11"), zones(2, 4, 2, "3")},
		{10, zones(3, 4, 3, "11"), zones(3, 4, 3, "3")},
		{3, zones(1, 1, 1, "11"), zones(1, 1, 1, "3")},
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

// zones spells out a zone split: g copies of the green's stand-in (the cyan, 6), y of
// the yellow's and o of the orange's (the normal yellow, 3), on either background.
func zones(g, y, o int, yellow string) []string {
	out := slices.Repeat([]string{"6"}, g)
	out = append(out, slices.Repeat([]string{yellow}, y)...)

	return append(out, slices.Repeat([]string{"3"}, o)...)
}

func TestRampDegenerateSizes(t *testing.T) {
	for _, n := range []int{-1, 0} {
		if got := Ramp(n); len(got) != 0 {
			t.Errorf("Ramp(%d) = %v, want empty", n, got)
		}
	}

	if got := Ramp(1); len(got) != 1 || got[0].Dark.TrueColor != "#00AAAA" {
		t.Errorf("Ramp(1) = %+v, want the single safe cyan", got)
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

// TestFailure pins how a failure is drawn: white on #E72300 (#FF2800 darkened to 4.5:1
// under white), xterm 231 on 160 at 256 colors and the bright white on the normal red at
// 16 colors, the same on either background.
func TestFailure(t *testing.T) {
	glyph := lipgloss.CompleteColor{TrueColor: "#FFFFFF", ANSI256: "231", ANSI: "15"}
	fill := lipgloss.CompleteColor{TrueColor: "#E72300", ANSI256: "160", ANSI: "1"}

	want := Cell{
		Fg: lipgloss.CompleteAdaptiveColor{Dark: glyph, Light: glyph},
		Bg: lipgloss.CompleteAdaptiveColor{Dark: fill, Light: fill},
	}
	if got := Failure(); got != want {
		t.Errorf("Failure() = %+v, want %+v", got, want)
	}
}

// TestFailureReadable checks the failure cell in 24-bit and 256-color form: the glyph
// keeps the 4.5:1 text contrast on its fill, and 3:1 to protan and deutan vision; the
// fill keeps 3:1 against both black and white, so it shows on either background.
func TestFailureReadable(t *testing.T) {
	f := Failure()

	pairs := []struct {
		name        string
		glyph, fill srgb
	}{
		{"24-bit", parseHex(t, f.Fg.Dark.TrueColor), parseHex(t, f.Bg.Dark.TrueColor)},
		{"256", parse256(t, f.Fg.Dark.ANSI256), parse256(t, f.Bg.Dark.ANSI256)},
	}
	for _, p := range pairs {
		g, b := luminance(p.glyph.linear()), luminance(p.fill.linear())
		if got := contrast(g, b); got < minTextContrast {
			t.Errorf("%s glyph %s on %s: contrast %.3f < %.1f",
				p.name, p.glyph.hex(), p.fill.hex(), got, minTextContrast)
		}

		for _, bg := range []float64{0, 1} {
			if got := contrast(b, bg); got < minContrast {
				t.Errorf("%s fill %s: contrast %.3f < %.1f against Y=%.0f",
					p.name, p.fill.hex(), got, minContrast, bg)
			}
		}

		for name, m := range map[string]*[3]vec{"protan": &protanSim, "deutan": &deutanSim} {
			g, b := luminance(mul(m, p.glyph.linear())), luminance(mul(m, p.fill.linear()))
			if got := contrast(g, b); got < minContrast {
				t.Errorf("%s glyph on fill seen by %s: contrast %.3f < %.1f",
					p.name, name, got, minContrast)
			}
		}
	}
}

// TestFailureApartFromRamp checks a failure never looks like a ramp level, at any color
// depth on either background: it is a filled cell, which no level is (Ramp gives glyph
// colors only), and its glyph color is one no level uses.
func TestFailureApartFromRamp(t *testing.T) {
	f := Failure()

	for _, fill := range []lipgloss.CompleteColor{f.Bg.Dark, f.Bg.Light} {
		if fill.TrueColor == "" || fill.ANSI256 == "" || fill.ANSI == "" {
			t.Errorf("Failure() fill %+v is empty at some depth", fill)
		}
	}

	for _, n := range []int{8, 10} {
		for i, c := range Ramp(n) {
			for _, p := range [][2]lipgloss.CompleteColor{{c.Dark, f.Fg.Dark}, {c.Light, f.Fg.Light}} {
				level, fail := p[0], p[1]
				if level.TrueColor == fail.TrueColor || level.ANSI256 == fail.ANSI256 ||
					level.ANSI == fail.ANSI {
					t.Errorf("Ramp(%d)[%d] %+v collides with Failure %+v", n, i, level, fail)
				}
			}
		}
	}
}

// linuxConsole is the Linux virtual console's default palette, indexed by ANSI color
// (drivers/tty/vt/vt.c default_red/grn/blu): the one 16-color terminal whose shades are
// known, and deadman's usual 16-color home. Its background is always dark.
var linuxConsole = [16]srgb{
	{0x00, 0x00, 0x00},
	{0xAA, 0x00, 0x00},
	{0x00, 0xAA, 0x00},
	{0xAA, 0x55, 0x00},
	{0x00, 0x00, 0xAA},
	{0xAA, 0x00, 0xAA},
	{0x00, 0xAA, 0xAA},
	{0xAA, 0xAA, 0xAA},
	{0x55, 0x55, 0x55},
	{0xFF, 0x55, 0x55},
	{0x55, 0xFF, 0x55},
	{0xFF, 0xFF, 0x55},
	{0x55, 0x55, 0xFF},
	{0xFF, 0x55, 0xFF},
	{0x55, 0xFF, 0xFF},
	{0xFF, 0xFF, 0xFF},
}

// Machado, Oliveira and Fernandes (2009) at severity 1, matrices on linear sRGB.
var (
	protanSim = [3]vec{
		{0.152286, 1.052583, -0.204868},
		{0.114503, 0.786281, 0.099216},
		{-0.003882, -0.048116, 1.051998},
	}
	deutanSim = [3]vec{
		{0.367322, 0.860646, -0.227968},
		{0.280085, 0.672501, 0.047413},
		{-0.011820, 0.042940, 0.968881},
	}
)

// TestANSIOnLinuxConsole checks the dark 16-color variant on the Linux console's palette:
// every ramp stand-in keeps 3:1 against black, the failure's glyph keeps the WCAG 2 text
// contrast of 4.5:1 on its fill, and the three stand-ins stay at least 0.1 apart in
// Oklab to normal, protan and deutan vision alike (the bright green the safe level used
// to take was 0.03 from the caution yellow to protan vision).
func TestANSIOnLinuxConsole(t *testing.T) {
	const (
		minTextContrast = 4.5
		minApart        = 0.1
	)

	vga := func(s string) srgb {
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 || i >= len(linuxConsole) {
			t.Fatalf("ANSI %q is not a 16-color index", s)
		}

		return linuxConsole[i]
	}

	stands := make([]srgb, len(anchors))
	for i, a := range anchors {
		stands[i] = vga(a.ansiDark)
		if got := contrast(luminance(stands[i].linear()), 0); got < minContrast {
			t.Errorf("anchor %d stand-in %s: contrast %.2f < %.1f on black",
				i, stands[i].hex(), got, minContrast)
		}
	}

	f := Failure()
	glyph, fill := vga(f.Fg.Dark.ANSI), vga(f.Bg.Dark.ANSI)

	if got := contrast(luminance(glyph.linear()), luminance(fill.linear())); got < minTextContrast {
		t.Errorf(
			"failure %s on %s: contrast %.2f < %.1f",
			glyph.hex(),
			fill.hex(),
			got,
			minTextContrast,
		)
	}

	visions := []struct {
		name string
		m    *[3]vec
	}{{"normal", nil}, {"protan", &protanSim}, {"deutan", &deutanSim}}
	for _, v := range visions {
		seen := func(c srgb) vec {
			if v.m == nil {
				return c.oklab()
			}

			return fromLinear(mul(v.m, c.linear()), math.Round).oklab()
		}

		for i := range stands {
			for j := i + 1; j < len(stands); j++ {
				if d := math.Sqrt(distSq(seen(stands[i]), seen(stands[j]))); d < minApart {
					t.Errorf("%s: anchors %d %s and %d %s only %.3f apart",
						v.name, i, stands[i].hex(), j, stands[j].hex(), d)
				}
			}
		}
	}
}
