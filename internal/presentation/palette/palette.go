// Package palette builds the RESULT bar's colors: the RTT ramp safe (cyan) → caution
// (yellow) → warning (orange), one color per bar level, and how a failed probe is drawn
// (white on a red fill).
//
// Red is Failure's alone, so the ramp stops at orange. The levels between the anchors
// are interpolated in Oklab and spaced by distance along the path, so the steps look
// even. Color is never the only cue: the glyph already encodes the level, and a failure
// is a filled cell, which no level is.
//
// Every color is given for each terminal color depth and background
// (lipgloss.CompleteAdaptiveColor) rather than left to lipgloss's down-conversion:
//   - 24-bit: the color itself on a dark background. On a light one it is darkened, the
//     slower the level the darker: from the WCAG 2 non-text contrast of 3:1 against
//     white at the fastest level to the text contrast of 4.5:1 at the slowest.
//   - 256 colors: the nearest xterm-256 entry that keeps 3:1 against the background,
//     skipping the 16 system colors, which themes redefine.
//   - 16 colors: the anchor's ANSI stand-in, whose exact shade the theme decides.
//
// Why each color was chosen (ISO 22324, CUD, color vision) is in
// docs/display.md.
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

// anchor is a ramp stop and its ANSI stand-ins on a dark and a light background.
type anchor struct {
	color               srgb
	ansiDark, ansiLight string
}

// anchors are the safe, caution and warning stops of the ramp: #00AAAA, a cyan, CUD
// recommended set ver.4 yellow, and #FF8000, an orange that stays one at 256 colors
// (xterm 208).
//
// The safe stop is a cyan rather than a green so that protan and deutan vision, which
// lose the red-green axis, still tell the fast end from the slow one by the blue-yellow
// axis: a green, even CUD's bluish #03AF7A, turns a dull yellow close to the orange's
// there. It is the Linux console's cyan, so its 16-color stand-in is the very same
// color. The yellow's stand-in is the bright yellow on a dark background and the normal
// one on a light one, where the bright one washes out. The 16 colors have no orange, so
// it borrows the normal yellow (brown on the Linux console).
var anchors = [...]anchor{
	{srgb{0x00, 0xAA, 0xAA}, "6", "6"},  // cyan: safe.
	{srgb{0xFF, 0xF1, 0x00}, "11", "3"}, // yellow: caution.
	{srgb{0xFF, 0x80, 0x00}, "3", "3"},  // orange: warning.
}

// failureRed is the red a failure is drawn in, CUD recommended set ver.3 #FF2800. It is
// given explicitly, not as the terminal's red, which some themes (Solarized) make an
// orange.
var failureRed = srgb{0xFF, 0x28, 0x00}

// A failure is a filled cell, a white glyph on a red fill, on either background. The fill
// tells a failure from every level by shape, whatever the color vision. At 24 bits the
// fill is failureRed darkened until the white reaches the text contrast of 4.5:1
// (#E72300; xterm 160 at 256 colors), which leaves it over 3:1 against black and white
// alike. At 16 colors it is the bright white on the normal red: 7.8:1 on the Linux
// console, whose red is only 2.7:1 against its black, but the fill covers the whole cell
// and does not have to be read.
const (
	failureANSIGlyph = "15"
	failureANSIFill  = "1"
)

// white is the failure glyph's color, and white256 its xterm-256 entry, the cube's
// last.
var white = srgb{maxChannel, maxChannel, maxChannel}

const white256 = cubeFirst + cubeSide*cubeSide*cubeSide - 1

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
// floors it at minLumOnDark (0.1). The text contrast caps it at maxLumTextOnLight
// (0.183) against white: the slowest level on a light background, and the failure fill
// under its white glyph.
const (
	minContrast       = 3.0 // SC 1.4.11 non-text contrast.
	minTextContrast   = 4.5 // SC 1.4.3 text contrast.
	flare             = 0.05
	maxLumOnLight     = (1+flare)/minContrast - flare
	minLumOnDark      = minContrast*flare - flare
	maxLumTextOnLight = (1+flare)/minTextContrast - flare
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

// Cell is how a RESULT-bar cell is drawn: its glyph's color and the fill behind it. A
// fill left empty keeps the terminal's background.
type Cell struct {
	Fg, Bg lipgloss.CompleteAdaptiveColor
}

// Failure returns how a failed probe (X/t/s/?) is drawn: a white glyph on a red fill at
// every depth, which no ramp level has.
func Failure() Cell {
	fill := capLuminance(failureRed, maxLumTextOnLight)

	glyph := lipgloss.CompleteColor{
		TrueColor: white.hex(),
		ANSI256:   strconv.Itoa(white256),
		ANSI:      failureANSIGlyph,
	}
	bg := lipgloss.CompleteColor{
		TrueColor: fill.hex(),
		ANSI256:   strconv.Itoa(nearest256(fill, underWhiteText)),
		ANSI:      failureANSIFill,
	}

	return Cell{
		Fg: lipgloss.CompleteAdaptiveColor{Dark: glyph, Light: glyph},
		Bg: lipgloss.CompleteAdaptiveColor{Dark: bg, Light: bg},
	}
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
// background. The 16-color stand-in goes by position alone, as if the anchors were
// evenly spaced: the fast and the slow quarter take the safe and the warning stand-in,
// the middle half the caution one.
func swatch(t float64) lipgloss.CompleteAdaptiveColor {
	near := anchors[int(math.Round(t*float64(len(anchors)-1)))]
	dark := rampAt(t)
	light := capLuminance(dark, lightCap(t))

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

// rampAt returns the color at position t in [0, 1] along the path through the anchors in
// Oklab. The position is measured by distance along the path, so evenly spaced
// positions give evenly spaced colors although the segments differ in length.
func rampAt(t float64) srgb {
	var stops [len(anchors)]vec
	for i, a := range anchors {
		stops[i] = a.color.oklab()
	}

	var lengths [len(anchors) - 1]float64

	total := 0.0

	for i := range lengths {
		lengths[i] = math.Sqrt(distSq(stops[i], stops[i+1]))
		total += lengths[i]
	}

	d, seg := min(max(t, 0), 1)*total, 0
	for seg < len(lengths)-1 && d > lengths[seg] {
		d -= lengths[seg]
		seg++
	}

	f := d / lengths[seg]

	var mixed vec
	for i := range mixed {
		mixed[i] = stops[seg][i] + (stops[seg+1][i]-stops[seg][i])*f
	}

	return fromLinear(oklabToLinear(mixed), math.Round)
}

// lightCap is the luminance a level at position t may reach on a light background: the
// 3:1 cap at the fastest level, the 4.5:1 cap at the slowest, and evenly spaced in
// lightness between them (a gray's Oklab lightness is the cube root of its luminance).
// The slower a level, the darker it reads, to any color vision.
func lightCap(t float64) float64 {
	from, to := math.Cbrt(maxLumOnLight), math.Cbrt(maxLumTextOnLight)
	l := from + (to-from)*t

	return l * l * l
}

// capLuminance scales c's linear light down, keeping its chromaticity, until its
// luminance is at most yMax. The channels are floored when re-encoded so rounding cannot
// push the luminance back over the cap.
func capLuminance(c srgb, yMax float64) srgb {
	lin := c.linear()

	y := luminance(lin)
	if y <= yMax {
		return c
	}

	k := yMax / y
	for i := range lin {
		lin[i] *= k
	}

	return fromLinear(lin, math.Floor)
}

// readableOnDark and readableOnLight report whether c reaches the minimum contrast
// against black and white respectively; underWhiteText, whether white text on c reaches
// the text contrast.
func readableOnDark(c srgb) bool { return luminance(c.linear()) >= minLumOnDark }

func readableOnLight(c srgb) bool { return luminance(c.linear()) <= maxLumOnLight }

func underWhiteText(c srgb) bool { return luminance(c.linear()) <= maxLumTextOnLight }

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
