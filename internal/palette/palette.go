// Package palette builds the RESULT bar's colors: the RTT ramp safe (green) → caution
// (yellow) → warning (orange), one color per bar level, and the red of a failed probe.
//
// Red is Failure's alone, so the ramp stops at orange. The levels between the anchors
// are interpolated in Oklab so the steps look even. Color is never the only cue, as the
// glyph already encodes the level.
//
// Every color is given for each terminal color depth and background
// (lipgloss.CompleteAdaptiveColor) rather than left to lipgloss's down-conversion:
//   - 24-bit: the color itself on a dark background; on a light one, darkened until it
//     reaches the WCAG 2 non-text contrast of 3:1 against white.
//   - 256 colors: the nearest xterm-256 entry that keeps the same 3:1, skipping the 16
//     system colors, which themes redefine.
//   - 16 colors: the anchor's ANSI stand-in, whose exact shade the theme decides.
//
// Why each color was chosen (ISO 22324, CUD, color vision) is in
// docs/platform_and_font.md.
package palette

import (
	"fmt"
	"math"
	"strconv"

	"github.com/charmbracelet/lipgloss"
)

// vec is a color as three components: linear-light sRGB, Oklab (L, a, b) or LMS.
type vec [3]float64

// srgb is a gamma-encoded 8-bit sRGB color, one int per channel in [0, maxChannel].
type srgb [3]int

// anchor is a base color (a ramp stop or the failure red) and its ANSI stand-ins on a
// dark and a light background.
type anchor struct {
	color               srgb
	ansiDark, ansiLight string
}

// anchors are the safe, caution and warning stops, evenly spaced along the ramp: CUD
// recommended set ver.4 green and yellow, and #FF8000, an orange that stays one at 256
// colors (xterm 208). The ANSI stand-ins are the bright colors on a dark background and
// the normal ones on a light one, where the bright ones wash out. The 16 colors have no
// orange, so it borrows the normal yellow (brown on the Linux console).
var anchors = [...]anchor{
	{srgb{0x03, 0xAF, 0x7A}, "10", "2"}, // green: safe.
	{srgb{0xFF, 0xF1, 0x00}, "11", "3"}, // yellow: caution.
	{srgb{0xFF, 0x80, 0x00}, "3", "3"},  // orange: warning.
}

// failure is the red of a failed probe, CUD recommended set ver.3 #FF2800 (xterm 196),
// readable on both backgrounds. It is given explicitly, not as the terminal's red, which
// some themes (Solarized) make an orange. On a dark background it takes the bright red,
// as the Linux console's normal red falls under 3:1 on black.
var failure = anchor{srgb{0xFF, 0x28, 0x00}, "9", "1"}

// Oklab's matrices (Björn Ottosson, "A perceptual color space for image processing",
// 2020): linear sRGB → LMS → (cube root) → Lab, and back.
var (
	linearToLMS = [3]vec{
		{0.4122214708, 0.5363325363, 0.0514459929},
		{0.2119034982, 0.6806995451, 0.1073969566},
		{0.0883024619, 0.2817188376, 0.6299787005},
	}
	lmsToLab = [3]vec{
		{0.2104542553, 0.7936177850, -0.0040720468},
		{1.9779984951, -2.4285922050, 0.4505937099},
		{0.0259040371, 0.7827717662, -0.8086757660},
	}
	labToLMS = [3]vec{
		{1, 0.3963377774, 0.2158037573},
		{1, -0.1055613458, -0.0638541728},
		{1, -0.0894841775, -1.2914855480},
	}
	lmsToLinear = [3]vec{
		{4.0767416621, -3.3077115913, 0.2309699292},
		{-1.2684380046, 2.6097574011, -0.3413193965},
		{-0.0041960863, -0.7034186147, 1.7076147010},
	}
)

// luminanceWeights give the relative luminance Y of a linear sRGB color, as WCAG 2
// defines it for the contrast ratio.
var luminanceWeights = vec{0.2126, 0.7152, 0.0722}

// sRGB transfer function (IEC 61966-2-1).
const (
	maxChannel      = 255
	encodedCutoff   = 0.04045   // encoded value below which the curve is linear.
	linearCutoff    = 0.0031308 // the same point in linear light.
	linearSlope     = 12.92
	gammaOffset     = 0.055
	gammaExponent   = 2.4
	gammaNormalizer = 1 + gammaOffset
)

// WCAG 2 contrast: (Y1 + flare) / (Y2 + flare) must reach minContrast. Against white
// (Y = 1) that caps a color's luminance at maxLumOnLight (0.3); against black (Y = 0) it
// floors it at minLumOnDark (0.1).
const (
	minContrast   = 3.0 // SC 1.4.11 non-text contrast.
	flare         = 0.05
	maxLumOnLight = (1+flare)/minContrast - flare
	minLumOnDark  = minContrast*flare - flare
)

// The xterm-256 palette past the 16 system colors: a 6×6×6 cube (16–231) and a gray
// ramp (232–255).
const (
	cubeFirst = 16
	cubeSide  = 6
	grayFirst = 232
	grayLast  = 255
	grayStart = 8
	grayStep  = 10
)

// cubeLevels are the channel values of the xterm-256 color cube.
var cubeLevels = [cubeSide]int{0x00, 0x5F, 0x87, 0xAF, 0xD7, 0xFF}

// Failure returns the color of a failed probe (X/t/s). No ramp level uses it.
func Failure() lipgloss.CompleteAdaptiveColor {
	return adaptive(failure.color, failure)
}

// Ramp returns the color of each of levels bar levels, from safe (level 0) to warning
// (level levels-1). A single level gets the safe color.
func Ramp(levels int) []lipgloss.CompleteAdaptiveColor {
	out := make([]lipgloss.CompleteAdaptiveColor, max(levels, 0))
	for i := range out {
		t := 0.0
		if levels > 1 {
			t = float64(i) / float64(levels-1)
		}

		out[i] = swatch(t)
	}

	return out
}

// swatch builds the ramp color at position t in [0, 1] for every color depth and
// background.
func swatch(t float64) lipgloss.CompleteAdaptiveColor {
	return adaptive(rampAt(t), anchors[int(math.Round(t*float64(len(anchors)-1)))])
}

// adaptive expands dark, a color for a dark background, to every color depth and
// background, with near's ANSI stand-ins for 16 colors.
func adaptive(dark srgb, near anchor) lipgloss.CompleteAdaptiveColor {
	light := darkenForLight(dark)

	return lipgloss.CompleteAdaptiveColor{
		Dark: lipgloss.CompleteColor{
			TrueColor: dark.hex(),
			ANSI256:   strconv.Itoa(nearest256(dark, readableOnDark)),
			ANSI:      near.ansiDark,
		},
		Light: lipgloss.CompleteColor{
			TrueColor: light.hex(),
			ANSI256:   strconv.Itoa(nearest256(light, readableOnLight)),
			ANSI:      near.ansiLight,
		},
	}
}

// rampAt interpolates the anchors at position t in [0, 1] in Oklab.
func rampAt(t float64) srgb {
	pos := t * float64(len(anchors)-1)
	seg := min(max(int(pos), 0), len(anchors)-2)
	f := pos - float64(seg)

	from, to := anchors[seg].color.oklab(), anchors[seg+1].color.oklab()

	var mixed vec
	for i := range mixed {
		mixed[i] = from[i] + (to[i]-from[i])*f
	}

	return fromLinear(oklabToLinear(mixed), math.Round)
}

// darkenForLight scales c's linear light down, keeping its chromaticity, until it
// reaches the minimum contrast against white. The channels are floored when
// re-encoded so rounding cannot push the luminance back over the cap.
func darkenForLight(c srgb) srgb {
	lin := c.linear()

	y := luminance(lin)
	if y <= maxLumOnLight {
		return c
	}

	k := maxLumOnLight / y
	for i := range lin {
		lin[i] *= k
	}

	return fromLinear(lin, math.Floor)
}

// readableOnDark and readableOnLight report whether c reaches the minimum contrast
// against black and white respectively.
func readableOnDark(c srgb) bool { return luminance(c.linear()) >= minLumOnDark }

func readableOnLight(c srgb) bool { return luminance(c.linear()) <= maxLumOnLight }

// nearest256 returns the xterm-256 index (16–255) nearest to c in Oklab among those
// readable says pass.
func nearest256(c srgb, readable func(srgb) bool) int {
	want := c.oklab()

	best, bestDist := -1, math.Inf(1)

	for i := cubeFirst; i <= grayLast; i++ {
		p := xterm256(i)
		if !readable(p) {
			continue
		}

		if d := distSq(want, p.oklab()); d < bestDist {
			best, bestDist = i, d
		}
	}

	return best
}

// xterm256 returns the color of xterm-256 index i, for i in [16, 255].
func xterm256(i int) srgb {
	if i >= grayFirst {
		v := grayStart + grayStep*(i-grayFirst)

		return srgb{v, v, v}
	}

	i -= cubeFirst

	return srgb{
		cubeLevels[i/(cubeSide*cubeSide)],
		cubeLevels[i/cubeSide%cubeSide],
		cubeLevels[i%cubeSide],
	}
}

func (c srgb) hex() string {
	return fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2])
}

// linear decodes c to linear-light sRGB in [0, 1].
func (c srgb) linear() vec {
	var v vec

	for i, ch := range c {
		e := float64(ch) / maxChannel
		if e <= encodedCutoff {
			v[i] = e / linearSlope
		} else {
			v[i] = math.Pow((e+gammaOffset)/gammaNormalizer, gammaExponent)
		}
	}

	return v
}

func (c srgb) oklab() vec {
	lms := mul(&linearToLMS, c.linear())
	for i := range lms {
		lms[i] = math.Cbrt(lms[i])
	}

	return mul(&lmsToLab, lms)
}

// fromLinear encodes linear-light v to 8-bit sRGB, clipping out-of-gamut components
// and quantizing each channel with quantize (math.Round or math.Floor).
func fromLinear(v vec, quantize func(float64) float64) srgb {
	var c srgb

	for i, l := range v {
		l = min(max(l, 0), 1)

		e := linearSlope * l
		if l > linearCutoff {
			e = gammaNormalizer*math.Pow(l, 1/gammaExponent) - gammaOffset
		}

		c[i] = int(quantize(e * maxChannel))
	}

	return c
}

func oklabToLinear(lab vec) vec {
	lms := mul(&labToLMS, lab)
	for i := range lms {
		lms[i] = lms[i] * lms[i] * lms[i]
	}

	return mul(&lmsToLinear, lms)
}

func luminance(lin vec) float64 {
	return dot(luminanceWeights, lin)
}

func mul(m *[3]vec, v vec) vec {
	return vec{dot(m[0], v), dot(m[1], v), dot(m[2], v)}
}

func dot(a, b vec) float64 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

func distSq(a, b vec) float64 {
	d0, d1, d2 := a[0]-b[0], a[1]-b[1], a[2]-b[2]

	return d0*d0 + d1*d1 + d2*d2
}
