package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// GlyphAuto is the -g / "glyph" value that lets the terminal decide the RESULT-bar glyph
// set: the block elements where they can render, else ASCII.
const GlyphAuto = "auto"

// GlyphChoices lists every -g / "glyph" value for usage and error text: auto first, then
// the glyph sets of resultbar, so the list cannot drift from resultbar.ParseBar.
func GlyphChoices() string {
	return strings.Join(append([]string{GlyphAuto}, resultbar.BarNames()...), ", ")
}

// CheckGlyph checks a -g value: auto or a resultbar glyph set, both case-insensitive like
// the config directive keywords. Its error is the usage message for any other value, so
// the command line reports it in this layer's words.
func CheckGlyph(s string) error {
	if strings.EqualFold(s, GlyphAuto) {
		return nil
	}

	if _, ok := resultbar.ParseBar(s); ok {
		return nil
	}

	return fmt.Errorf("unknown glyph set %q (want one of: %s)", s, GlyphChoices())
}

// Flags are the display settings given on the command line. A numeric flag's zero value
// is also a value one can write, so whether it was given is its own field: an unusable
// given value is worth a warning, an omitted one lets the config directive or the
// default apply.
type Flags struct {
	Scale    float64 // -s: RTT-bar ms-per-step (the window floor in log mode).
	ScaleSet bool    // -s was given.
	Glyph    string  // -g: a CheckGlyph value; "" = not given.
	Cols     int     // -c: newspaper-column count.
	ColsSet  bool    // -c was given.
}

// resolveDisplay combines the command-line flags with the config's display directives:
// an explicit, usable flag wins, else a usable directive, else the default. Every value
// is validated here, against the vocabularies this layer owns — the RTT-bar scale
// window, the precision modes, the glyph sets, the column keys — so an unusable
// directive falls back to the default, and an unusable -s or -c is also reported in the
// warnings, which stay above the rows for the whole run. blockOK tells whether the
// terminal renders the block elements; it is asked only when the glyph set is auto,
// keeping the terminal probe off an explicit choice. A nil blockOK counts as renderable,
// as an unknown terminal does.
func resolveDisplay(f Flags, d config.Display, blockOK func() bool) (viewPrefs, []string) {
	prefs := viewPrefs{
		scale:   resolveScale(f.Scale, d.Scale),
		precIdx: precisionIndex(d.Precision),
		bar:     resolveGlyph(f.Glyph, d.Glyph, blockOK),
		cols:    resolveCols(f.Cols, d.Cols),
		visible: buildVisible(d.Columns),
	}

	var warnings []string
	if f.ScaleSet && !resultbar.ValidScale(f.Scale) {
		warnings = append(warnings, scaleFlagWarning(f.Scale))
	}

	if f.ColsSet && f.Cols <= 0 {
		warnings = append(warnings, colsFlagWarning(f.Cols))
	}

	return prefs, warnings
}

// resolveScale picks the RTT-bar scale: a usable -s, else a usable "scale", else the
// default. An unusable value (0, negative, non-finite, out of range) is dropped rather
// than flattening every bar.
func resolveScale(cli, cfg float64) float64 {
	if resultbar.ValidScale(cli) {
		return cli
	}

	return resultbar.ScaleOrDefault(cfg)
}

// resolveGlyph picks the RESULT-bar glyph set: a named -g wins, else a named "glyph",
// else auto — as does an unknown directive value, or an explicit -g auto, which
// overrides a set named in the config. Auto uses the block elements where blockOK says
// the terminal renders them and falls back to ASCII, which keeps the same levels and
// thresholds, so -s/scale means the same either way.
func resolveGlyph(cli, cfg string, blockOK func() bool) resultbar.Bar {
	choice := cli
	if choice == "" {
		choice = cfg
	}

	if b, ok := resultbar.ParseBar(choice); ok {
		return b
	}

	if blockOK == nil || blockOK() {
		return resultbar.BarBlock
	}

	return resultbar.BarASCII
}

// resolveCols picks the newspaper-column count: a positive -c, else a positive "split",
// else a single column. An unusable -c is reported by colsFlagWarning.
func resolveCols(cli, cfg int) int {
	switch {
	case cli > 0:
		return cli
	case cfg > 0:
		return cfg
	default:
		return 1
	}
}

// colsFlagWarning explains an explicitly passed but unusable -c value, like
// scaleFlagWarning, without naming the effective count (the '['/']' keys change it and the
// keys line shows it).
func colsFlagWarning(cli int) string {
	return fmt.Sprintf(
		"-c %d ignored: not a positive column count; using the configured or default split",
		cli,
	)
}

// scaleFlagWarning explains an explicitly passed but unusable -s value. It names the
// rejected value and the usable window — formatted from resultbar's bounds so the prose
// can't drift from the predicate — but deliberately NOT the effective scale: the warning
// is rendered persistently in the header while the live scale can still change (up/down),
// so embedding "using Nms" would go stale. The footer's "RTT Scale" line always shows
// the value in effect.
//
// The rejected value uses %g while the bounds use FormatFloat 'f' on purpose: the value
// is arbitrary operator input that may be extreme, and %g keeps -s 1e-300 a compact
// "1e-300" rather than the ~300-digit decimal 'f' would emit (the very label ballooning
// MinScale exists to reject); the bounds are known round numbers that read cleanest in
// plain 'f' (0.0001..1000000, no "1e+06").
func scaleFlagWarning(cli float64) string {
	return fmt.Sprintf(
		"-s %g ignored: not a usable RTT-bar scale (%s..%s ms); using the configured or default scale",
		cli,
		strconv.FormatFloat(resultbar.MinScale, 'f', -1, 64),
		strconv.FormatFloat(resultbar.MaxScale, 'f', -1, 64),
	)
}
