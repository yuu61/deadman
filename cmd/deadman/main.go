// Command deadman is a cross-platform (Windows/Linux/macOS) TUI host-status
// monitor that probes hosts with ICMP echo (or a relay) and renders their
// reachability, loss, and RTT as a live result bar.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/monitor"
	"github.com/yuu61/deadman/internal/tui"
)

// version is the build version shown in the TUI title bar. It is overridden at build
// time via -ldflags "-X main.version=..." (see the Makefile, which derives it from git
// describe); a plain `go install`/`go build` leaves it at "dev".
var version = "dev"

// resolveScale picks the effective RTT-bar scale: an explicit, usable CLI -s wins, else a
// config "scale" directive, else config.DefaultScale. 0 means "unset" for both inputs (the
// -s flag defaults to 0, and a missing or invalid "scale" directive leaves cfg.Scale at 0),
// so a config scale can take effect when -s is absent.
//
// Both inputs go through the same config.ValidScale predicate the "scale" directive uses,
// so a non-finite or out-of-range value (e.g. -s Inf, which flag.Float64 accepts) is
// dropped rather than flattening every bar. The rejection is not fatal and is surfaced as
// a startup warning by parseArgs (which alone can tell an explicit -s 0 from an unset one).
func resolveScale(cli, cfg float64) float64 {
	if config.ValidScale(cli) {
		return cli
	}

	return config.ScaleOrDefault(cfg)
}

// scaleWarning builds the operator-facing warning for an explicitly-passed but unusable -s
// value (0, negative, inf, nan, or out of range). It names the rejected value and the
// usable window — formatted from config's bounds so the prose can't drift from the
// predicate — but deliberately NOT the effective scale: the warning is rendered
// persistently in the header while the live scale can still change (↑/↓), so embedding
// "using Nms" would go stale. The footer's "RTT Scale" line always shows the value in
// effect, so the operator can read the real scale there.
//
// The rejected value uses %g while the bounds use FormatFloat 'f' on purpose, not by
// oversight: the value is arbitrary operator input that may be extreme, and %g keeps
// -s 1e-300 a compact "1e-300" rather than the ~300-digit decimal 'f' would emit (the
// very label ballooning MinScale exists to reject); the bounds are known round numbers
// that read cleanest in plain 'f' (0.0001..1000000, no "1e+06").
func scaleWarning(cli float64) string {
	return fmt.Sprintf(
		"-s %g ignored: not a usable RTT-bar scale (%s..%s ms); using the configured or default scale",
		cli,
		strconv.FormatFloat(config.MinScale, 'f', -1, 64),
		strconv.FormatFloat(config.MaxScale, 'f', -1, 64),
	)
}

// parseArgs parses the command line into TUI options. Flags may appear before or
// after the configfile; Go's flag package stops at the first non-flag argument, so
// we collect positionals and re-parse the remainder to let flags and the
// configfile intermix.
func parseArgs(args []string) (tui.Options, error) {
	fs := flag.NewFlagSet("deadman", flag.ContinueOnError)
	scaleUsage := fmt.Sprintf(
		"scale of ping RTT bar gap (ms, default %g, decimals allowed)",
		config.DefaultScale,
	)
	scale := fs.Float64("s", 0, scaleUsage)
	fs.Float64Var(scale, "scale", 0, scaleUsage)
	async := fs.Bool("a", false, "send ping asynchronously")
	fs.BoolVar(async, "async-mode", false, "send ping asynchronously")
	blink := fs.Bool("b", false, "blink arrow in async mode")
	fs.BoolVar(blink, "blink-arrow", false, "blink arrow in async mode")
	logdir := fs.String("l", "", "directory for log files")
	fs.StringVar(logdir, "logging", "", "directory for log files")
	cols := fs.Int("c", 0, "split the host list into N side-by-side columns (default 1)")
	fs.IntVar(cols, "split", 0, "split the host list into N side-by-side columns (default 1)")

	var positional []string

	rest := args
	for {
		err := fs.Parse(rest)
		if err != nil {
			return tui.Options{}, err
		}

		rest = fs.Args()
		if len(rest) == 0 {
			break
		}

		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	if len(positional) < 1 {
		return tui.Options{}, errors.New("configfile is required")
	}
	// Exactly one configfile is accepted. Erroring on extras (rather than silently
	// using the first) also closes a `--` foot-gun: `deadman -- -a cfg.conf` previously
	// dropped cfg.conf and ran the nonexistent file "-a".
	if len(positional) > 1 {
		return tui.Options{}, fmt.Errorf(
			"only one configfile may be given, got %d: %v",
			len(positional), positional,
		)
	}

	opts := tui.Options{
		Async:      *async,
		Blink:      *blink,
		Scale:      *scale,
		LogDir:     *logdir,
		Cols:       *cols,
		ConfigPath: positional[0],
	}

	// Surface an explicitly-passed but unusable -s as a startup warning. The value is still
	// dropped and the configured/default scale is used (see resolveScale); this only makes
	// the silently-ignored input visible.
	if scaleFlagSet(fs) && !config.ValidScale(*scale) {
		opts.Warnings = append(opts.Warnings, scaleWarning(*scale))
	}

	return opts, nil
}

// scaleFlagSet reports whether -s or --scale was actually given on the command line. It
// distinguishes an explicit `-s 0` (a rejected value worth warning about) from an unset -s
// — the *scale == 0 sentinel cannot, since both leave it at the flag's 0 default — by
// asking the flag set which flags were set rather than inspecting the parsed value.
func scaleFlagSet(fs *flag.FlagSet) bool {
	set := false

	fs.Visit(func(f *flag.Flag) {
		if f.Name == "s" || f.Name == "scale" {
			set = true
		}
	})

	return set
}

// resolveCols picks the newspaper-column count: an explicit CLI -c/--split wins,
// else a config "split" directive, else a single column. 0 means "unset" for both
// inputs (the -c flag defaults to 0, and a missing/invalid "split" directive leaves
// cfg at 0), and tui.New normalizes a non-positive count to 1.
func resolveCols(cli, cfg int) int {
	if cli > 0 {
		return cli
	}

	return cfg
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h/--help: the flag package already printed usage; exit success like
			// flag.ExitOnError would, rather than reporting "flag: help requested".
			os.Exit(0)
		}

		fmt.Fprintln(os.Stderr, "usage: deadman [options] configfile")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	f, err := os.Open(opts.ConfigPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg, err := config.ParseConfig(f)
	_ = f.Close()

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// The invalid-scale warning (if any) was already queued by parseArgs, which alone can
	// tell an explicit -s from its default; here we only resolve the effective value.
	opts.Columns = cfg.Columns
	opts.Scale = resolveScale(opts.Scale, cfg.Scale)
	opts.Precision = cfg.Precision
	opts.Cols = resolveCols(opts.Cols, cfg.Cols)
	opts.Version = version

	if opts.LogDir != "" {
		opts.LogWriter = monitor.NewLogWriter(opts.LogDir)
	}

	m, err := tui.New(cfg.Targets, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	tui.InstallReloadSignal(p)

	_, err = p.Run()

	// Drain any queued log lines before exit. The TUI has stopped, so no Log call races
	// this Close. os.Exit skips defers, so close explicitly before the error exit too.
	if opts.LogWriter != nil {
		opts.LogWriter.Close()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
