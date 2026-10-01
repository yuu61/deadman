package tui

import (
	"math"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// TestResolveScale pins the effective scale: a usable -s wins, else a usable "scale"
// directive, else the default; any unusable value is dropped rather than flattening
// every bar. The directive arrives as written (the parser checks only that it is a
// number), so the range rules are applied here.
func TestResolveScale(t *testing.T) {
	cases := []struct {
		name     string
		cli, cfg float64
		want     float64
	}{
		{"cli explicit wins over config", 20, 5, 20},
		{"config used when cli unset", 0, 5, 5},
		{"cli used when config unset", 7, 0, 7},
		{"sub-ms cli is honored", 0.5, 0, 0.5},
		{"non-finite cli falls back to config", math.Inf(1), 5, 5},
		{"nan cli falls back to config", math.NaN(), 5, 5},
		{"out-of-range cli falls back to default", 1e-300, 0, resultbar.DefaultScale},
		{"negative cfg falls back to default", 0, -3, resultbar.DefaultScale},
		{"out-of-range cfg falls back to default", 0, 1e300, resultbar.DefaultScale},
		{"non-finite cfg falls back to default", 0, math.Inf(1), resultbar.DefaultScale},
		{"nan cfg falls back to default", 0, math.NaN(), resultbar.DefaultScale},
		{"default when both unset", 0, 0, resultbar.DefaultScale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveScale(c.cli, c.cfg); got != c.want {
				t.Errorf("resolveScale(%g, %g) = %g, want %g", c.cli, c.cfg, got, c.want)
			}
		})
	}
}

// An explicitly passed, unusable -s surfaces a warning while an unset or usable -s stays
// silent. ScaleSet (from the flag layer) is what tells an explicit -s 0 from an unset
// one. The warning omits the effective scale on purpose (it is rendered persistently
// while ↑/↓ can change the live scale), so it names only the value and the window.
func TestResolveDisplayScaleWarning(t *testing.T) {
	cases := []struct {
		name     string
		flags    Flags
		wantWarn bool
	}{
		{"unset -s is silent", Flags{}, false},
		{"usable -s is silent", Flags{Scale: 5, ScaleSet: true}, false},
		{"explicit zero -s warns", Flags{Scale: 0, ScaleSet: true}, true},
		{"negative -s warns", Flags{Scale: -5, ScaleSet: true}, true},
		{"inf -s warns", Flags{Scale: math.Inf(1), ScaleSet: true}, true},
		{"nan -s warns", Flags{Scale: math.NaN(), ScaleSet: true}, true},
		{"out-of-range -s warns", Flags{Scale: 1e-300, ScaleSet: true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, warnings := resolveDisplay(c.flags, config.Display{}, nil)
			if gotWarn := len(warnings) > 0; gotWarn != c.wantWarn {
				t.Fatalf("warnings = %v, wantWarn=%v", warnings, c.wantWarn)
			}

			if c.wantWarn && !strings.Contains(warnings[0], "0.0001..1000000 ms") {
				t.Errorf("warning %q does not name the usable window", warnings[0])
			}
		})
	}
}

func TestResolveCols(t *testing.T) {
	cases := []struct {
		name     string
		cli, cfg int
		want     int
	}{
		{"cli explicit wins over config", 3, 2, 3},
		{"config used when cli unset", 0, 2, 2},
		{"cli used when config unset", 2, 0, 2},
		{"non-positive config falls back to one column", 0, -2, 1},
		{"one column when both unset", 0, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveCols(c.cli, c.cfg); got != c.want {
				t.Errorf("resolveCols(%d, %d) = %d, want %d", c.cli, c.cfg, got, c.want)
			}
		})
	}
}

// TestResolveGlyph pins the precedence (explicit CLI -g > config "glyph" > auto) and the
// auto policy (block where the terminal can render it, else ascii). The probe must not
// run when a set is named explicitly: it inspects the real terminal, so a spurious call
// would make an explicit choice environment-dependent.
func TestResolveGlyph(t *testing.T) {
	cases := []struct {
		name      string
		cli, cfg  string
		blockOK   bool
		wantProbe bool
		want      resultbar.Bar
	}{
		{"cli wins over config", "digit", "ascii", true, false, resultbar.BarDigit},
		{"cli case-folds", "DIGIT", "", true, false, resultbar.BarDigit},
		{"config used when cli unset", "", "digit", true, false, resultbar.BarDigit},
		{"cli auto overrides config set", "auto", "digit", false, true, resultbar.BarASCII},
		{"config auto probes", "", "auto", true, true, resultbar.BarBlock},
		{"unknown config falls to auto", "", "bogus", false, true, resultbar.BarASCII},
		{"unset probes: renderable", "", "", true, true, resultbar.BarBlock},
		{"unset probes: not renderable", "", "", false, true, resultbar.BarASCII},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probed := false
			got := resolveGlyph(c.cli, c.cfg, func() bool {
				probed = true

				return c.blockOK
			})

			if got != c.want {
				t.Errorf("resolveGlyph(%q, %q, blockOK=%v) = %v, want %v",
					c.cli, c.cfg, c.blockOK, got, c.want)
			}

			if probed != c.wantProbe {
				t.Errorf("probe called = %v, want %v", probed, c.wantProbe)
			}
		})
	}

	if got := resolveGlyph("", "", nil); got != resultbar.BarBlock {
		t.Errorf("auto without a terminal probe = %v, want block", got)
	}
}

// The glyph vocabulary the CLI validates -g against is auto plus resultbar's sets.
func TestGlyphChoices(t *testing.T) {
	if got := GlyphChoices(); got != "auto, block, ascii, digit" {
		t.Errorf("GlyphChoices = %q", got)
	}

	for _, s := range []string{"auto", "AUTO", "block", "Digit"} {
		err := CheckGlyph(s)
		if err != nil {
			t.Errorf("CheckGlyph(%q) = %v, want nil", s, err)
		}
	}

	for _, s := range []string{"", "digits"} {
		err := CheckGlyph(s)
		if err == nil || !strings.Contains(err.Error(), GlyphChoices()) {
			t.Errorf("CheckGlyph(%q) = %v, want an error naming the choices", s, err)
		}
	}
}

// An explicitly passed -c that is not a positive count is reported, like an unusable -s,
// instead of being taken silently for an omitted one.
func TestResolveDisplayColsWarning(t *testing.T) {
	for _, c := range []struct {
		flags    Flags
		wantWarn bool
	}{
		{Flags{}, false},
		{Flags{Cols: 2, ColsSet: true}, false},
		{Flags{Cols: 0, ColsSet: true}, true},
		{Flags{Cols: -3, ColsSet: true}, true},
	} {
		d, warnings := resolveDisplay(c.flags, config.Display{Cols: 2}, nil)
		if gotWarn := len(warnings) > 0; gotWarn != c.wantWarn || d.cols != max(c.flags.Cols, 2) {
			t.Errorf("%+v: warnings=%v cols=%d", c.flags, warnings, d.cols)
		}
	}
}

// Every directive value is matched case-insensitively, the precision label included.
func TestPrecisionLabelIgnoresCase(t *testing.T) {
	if precisionModes[precisionIndex("MS.2")].Label != "ms.2" {
		t.Error("precision MS.2 fell back to the default")
	}
}

// Every directive is validated against this layer's vocabulary: an unknown precision
// label or a glyph name falls back, like an unusable number.
func TestResolveDisplayDirectives(t *testing.T) {
	d, _ := resolveDisplay(Flags{}, config.Display{
		Scale: 5, Precision: "ms.2", Glyph: "ascii", Cols: 2,
		Columns: map[string]bool{"MIN": false},
	}, nil)
	if d.scale != 5 || precisionModes[d.precIdx].Label != "ms.2" || d.bar != resultbar.BarASCII ||
		d.cols != 2 || d.visible["MIN"] || !d.visible["MAX"] {
		t.Errorf("resolved = %+v, want the directives applied", d)
	}

	d, _ = resolveDisplay(Flags{}, config.Display{Precision: "ms.9"}, nil)
	if d.precIdx != 0 {
		t.Errorf("an unknown precision label resolved to mode %d, want ms", d.precIdx)
	}
}
