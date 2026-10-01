package main

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
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
			args, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if args.Async != c.async {
				t.Errorf("Async = %v, want %v", args.Async, c.async)
			}

			if args.ConfigPath != c.path {
				t.Errorf("ConfigPath = %q, want %q", args.ConfigPath, c.path)
			}
		})
	}
}

func TestParseArgsScaleBlinkLog(t *testing.T) {
	args, err := parseArgs([]string{"deadman.conf", "-s", "20", "-b", "-l", "logs"})
	if err != nil {
		t.Fatal(err)
	}

	if args.Display.Scale != 20 {
		t.Errorf("Scale = %g, want 20", args.Display.Scale)
	}

	if !args.Blink {
		t.Error("Blink = false, want true")
	}

	if args.LogDir != "logs" {
		t.Errorf("LogDir = %q, want logs", args.LogDir)
	}
}

func TestParseArgsScaleFractional(t *testing.T) {
	// A fractional -s is accepted (sub-ms scale); the flag parses as float64.
	args, err := parseArgs([]string{"deadman.conf", "-s", "0.5"})
	if err != nil {
		t.Fatal(err)
	}

	if args.Display.Scale != 0.5 {
		t.Errorf("Scale = %g, want 0.5", args.Display.Scale)
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
			args, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if args.Display.Cols != c.want {
				t.Errorf("Cols = %d, want %d", args.Display.Cols, c.want)
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
			args, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if args.Display.Glyph != c.want {
				t.Errorf("Glyph = %q, want %q", args.Display.Glyph, c.want)
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

// parseArgs records whether -s was given at all, which the TUI needs to warn about an
// explicit but unusable value: an explicit `-s 0` and an unset -s both parse to 0, so
// only fs.Visit can tell them apart.
func TestParseArgsScaleSet(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"unset", []string{"deadman.conf"}, false},
		{"usable -s", []string{"-s", "5", "deadman.conf"}, true},
		{"explicit zero -s", []string{"-s", "0", "deadman.conf"}, true},
		{"explicit zero --scale", []string{"--scale=0", "deadman.conf"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if args.Display.ScaleSet != c.want {
				t.Errorf("ScaleSet = %v, want %v", args.Display.ScaleSet, c.want)
			}
		})
	}
}

// parseArgs records whether -c was given at all, for the same reason as -s.
func TestParseArgsColsSet(t *testing.T) {
	for args, want := range map[string]bool{
		"deadman.conf":           false,
		"-c 2 deadman.conf":      true,
		"-c 0 deadman.conf":      true,
		"--split=0 deadman.conf": true,
	} {
		got, err := parseArgs(strings.Fields(args))
		if err != nil {
			t.Fatal(err)
		}

		if got.Display.ColsSet != want {
			t.Errorf("%s: ColsSet = %v, want %v", args, got.Display.ColsSet, want)
		}
	}
}
