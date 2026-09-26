package main

import (
	"errors"
	"flag"
	"math"
	"os"
	"testing"

	"github.com/yuu61/deadman/internal/config"
)

// -h/--help must surface flag.ErrHelp so main can exit 0 (success) rather than the
// generic exit-2 error path.
func TestParseArgsHelp(t *testing.T) {
	// flag writes its usage to os.Stderr on -h/--help; redirect it to a pipe so the
	// test output stays quiet (only the returned error matters here).
	orig := os.Stderr

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = w

	defer func() {
		os.Stderr = orig
		_ = w.Close()
		_ = r.Close()
	}()

	for _, arg := range []string{"-h", "--help"} {
		_, perr := parseArgs([]string{arg})
		if !errors.Is(perr, flag.ErrHelp) {
			t.Errorf("parseArgs(%q) error = %v, want flag.ErrHelp", arg, perr)
		}
	}
}

func TestParseArgsAsync(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		async bool
		path  string
	}{
		{"flag before config", []string{"-a", "deadman.conf"}, true, "deadman.conf"},
		{"flag after config", []string{"deadman.conf", "-a"}, true, "deadman.conf"},
		{"long form", []string{"--async-mode", "deadman.conf"}, true, "deadman.conf"},
		{"no flag", []string{"deadman.conf"}, false, "deadman.conf"},
		{"mixed", []string{"-s", "20", "deadman.conf", "-a", "-l", "logs"}, true, "deadman.conf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if opts.Async != c.async {
				t.Errorf("Async = %v, want %v", opts.Async, c.async)
			}

			if opts.ConfigPath != c.path {
				t.Errorf("ConfigPath = %q, want %q", opts.ConfigPath, c.path)
			}
		})
	}
}

func TestParseArgsScaleBlinkLog(t *testing.T) {
	opts, err := parseArgs([]string{"deadman.conf", "-s", "20", "-b", "-l", "logs"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.Scale != 20 {
		t.Errorf("Scale = %g, want 20", opts.Scale)
	}

	if !opts.Blink {
		t.Error("Blink = false, want true")
	}

	if opts.LogDir != "logs" {
		t.Errorf("LogDir = %q, want logs", opts.LogDir)
	}
}

func TestParseArgsScaleFractional(t *testing.T) {
	// A fractional -s is accepted (sub-ms scale); the flag parses as float64.
	opts, err := parseArgs([]string{"deadman.conf", "-s", "0.5"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.Scale != 0.5 {
		t.Errorf("Scale = %g, want 0.5", opts.Scale)
	}
}

func TestParseArgsMissingConfig(t *testing.T) {
	_, err := parseArgs([]string{"-a"})
	if err == nil {
		t.Error("expected error when configfile is missing")
	}
}

// Exactly one configfile is accepted; extra positionals are an error rather than a
// silent drop. This also makes `--` foot-guns explicit instead of losing a config path.
func TestParseArgsRejectsMultipleConfigs(t *testing.T) {
	cases := [][]string{
		{"a.conf", "b.conf"},
		{"--", "-a", "cfg.conf"}, // previously dropped cfg.conf and ran "-a".
	}
	for _, args := range cases {
		_, err := parseArgs(args)
		if err == nil {
			t.Errorf("parseArgs(%v) = nil error, want rejection of multiple configfiles", args)
		}
	}
}

// TestResolveScale pins the effective scale: an explicit, usable CLI -s wins, else the
// config "scale", else the default; any unusable value (here passed as a bare float, not
// via the flag) is dropped rather than flattening every bar. The companion warning is the
// flag layer's job — it needs to tell an explicit -s from its default — so it is covered
// by TestParseArgsScaleWarning, not here.
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
		{"out-of-range cli falls back to default", 1e-300, 0, config.DefaultScale},
		{"non-finite cfg falls back to default", 0, math.Inf(1), config.DefaultScale},
		{"nan cfg falls back to default", 0, math.NaN(), config.DefaultScale},
		{"default when both unset", 0, 0, config.DefaultScale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveScale(c.cli, c.cfg); got != c.want {
				t.Errorf("resolveScale(%g, %g) = %g, want %g", c.cli, c.cfg, got, c.want)
			}
		})
	}
}

// TestParseArgsScaleWarning checks that an explicitly-passed, unusable -s surfaces a
// startup warning while an unset or usable -s stays silent. The explicit `-s 0` case is
// the one a value sentinel cannot catch (an unset -s also parses to 0): fs.Visit
// distinguishes them, so the rejected zero is not silently dropped. The warning omits the
// effective scale on purpose (it is rendered persistently while ↑/↓ can change the live
// scale), so this only asserts presence/absence, not an embedded "using Nms".
func TestParseArgsScaleWarning(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantWarn bool
	}{
		{"unset -s is silent", []string{"deadman.conf"}, false},
		{"usable -s is silent", []string{"-s", "5", "deadman.conf"}, false},
		{"explicit zero -s warns", []string{"-s", "0", "deadman.conf"}, true},
		{"explicit zero --scale warns", []string{"--scale=0", "deadman.conf"}, true},
		{"negative -s warns", []string{"-s", "-5", "deadman.conf"}, true},
		{"inf -s warns", []string{"-s", "inf", "deadman.conf"}, true},
		{"nan -s warns", []string{"-s", "nan", "deadman.conf"}, true},
		{"out-of-range -s warns", []string{"-s", "1e-300", "deadman.conf"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if gotWarn := len(opts.Warnings) > 0; gotWarn != c.wantWarn {
				t.Errorf(
					"parseArgs(%v) warnings = %v, wantWarn=%v",
					c.args, opts.Warnings, c.wantWarn,
				)
			}
		})
	}
}

func TestParseArgsSplit(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"short flag", []string{"-c", "2", "deadman.conf"}, 2},
		{"long flag", []string{"--split", "3", "deadman.conf"}, 3},
		{"after config", []string{"deadman.conf", "-c", "2"}, 2},
		{"unset defaults to 0", []string{"deadman.conf"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if opts.Cols != c.want {
				t.Errorf("Cols = %d, want %d", opts.Cols, c.want)
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
		{"unset stays 0 (New normalizes to 1)", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveCols(c.cli, c.cfg); got != c.want {
				t.Errorf("resolveCols(%d, %d) = %d, want %d", c.cli, c.cfg, got, c.want)
			}
		})
	}
}

func TestParseArgsGlyph(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"short flag", []string{"-g", "digit", "deadman.conf"}, "digit"},
		{"long flag", []string{"--glyph=ascii", "deadman.conf"}, "ascii"},
		{"after config", []string{"deadman.conf", "-g", "block"}, "block"},
		{"explicit auto", []string{"-g", "auto", "deadman.conf"}, "auto"},
		{"case-insensitive", []string{"-g", "DIGIT", "deadman.conf"}, "DIGIT"},
		{"unset stays empty", []string{"deadman.conf"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if opts.Glyph != c.want {
				t.Errorf("Glyph = %q, want %q", opts.Glyph, c.want)
			}
		})
	}
}

// An unknown -g value is a usage error, not a silent fallback: unlike the lenient
// config directive, the operator typed it on this very invocation.
func TestParseArgsGlyphRejectsUnknown(t *testing.T) {
	// flag reports the error and usage on os.Stderr; keep the test output quiet.
	orig := os.Stderr

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = w

	defer func() {
		os.Stderr = orig
		_ = w.Close()
		_ = r.Close()
	}()

	for _, args := range [][]string{{"-g", "digits", "deadman.conf"}, {"--glyph", "", "deadman.conf"}} {
		_, perr := parseArgs(args)
		if perr == nil {
			t.Errorf("parseArgs(%v) = nil error, want an unknown glyph set rejection", args)
		}
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
		want      string
	}{
		{"cli wins over config", "digit", "ascii", true, false, "digit"},
		{"cli case-folds", "DIGIT", "", true, false, "digit"},
		{"config used when cli unset", "", "digit", true, false, "digit"},
		{"cli auto overrides config set", "auto", "digit", false, true, "ascii"},
		{"config auto probes", "", "auto", true, true, "block"},
		{"unknown config falls to auto", "", "bogus", false, true, "ascii"},
		{"unset probes: renderable", "", "", true, true, "block"},
		{"unset probes: not renderable", "", "", false, true, "ascii"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probed := false
			got := resolveGlyph(c.cli, c.cfg, func() bool {
				probed = true

				return c.blockOK
			})

			if got != c.want {
				t.Errorf(
					"resolveGlyph(%q, %q, blockOK=%v) = %q, want %q",
					c.cli,
					c.cfg,
					c.blockOK,
					got,
					c.want,
				)
			}

			if probed != c.wantProbe {
				t.Errorf("probe called = %v, want %v", probed, c.wantProbe)
			}
		})
	}
}
