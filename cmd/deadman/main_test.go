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
