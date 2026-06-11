// Command deadman is a cross-platform (Windows/Linux/macOS) TUI host-status
// monitor that probes hosts with ICMP echo (or a relay) and renders their
// reachability, loss, and RTT as a live result bar.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/tui"
)

// defaultScale is the default RTT bar gap in milliseconds.
const defaultScale = 10

// resolveScale picks the effective RTT-bar scale: an explicit CLI -s wins, else a
// config "scale" directive, else defaultScale. 0 means "unset" for both inputs (the
// -s flag defaults to 0, and a missing or invalid "scale" directive leaves cfg.Scale
// at 0), so passing -s is never indistinguishable from its own default and a config
// scale can take effect.
func resolveScale(cli, cfg int) int {
	if cli > 0 {
		return cli
	}

	if cfg > 0 {
		return cfg
	}

	return defaultScale
}

// parseArgs parses the command line into TUI options. Flags may appear before or
// after the configfile; Go's flag package stops at the first non-flag argument, so
// we collect positionals and re-parse the remainder to let flags and the
// configfile intermix.
func parseArgs(args []string) (tui.Options, error) {
	fs := flag.NewFlagSet("deadman", flag.ContinueOnError)
	scale := fs.Int("s", 0, "scale of ping RTT bar gap (ms, default 10)")
	fs.IntVar(scale, "scale", 0, "scale of ping RTT bar gap (ms, default 10)")
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

	return tui.Options{
		Async:      *async,
		Blink:      *blink,
		Scale:      *scale,
		LogDir:     *logdir,
		Cols:       *cols,
		ConfigPath: positional[0],
	}, nil
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

	opts.Columns = cfg.Columns
	opts.Scale = resolveScale(opts.Scale, cfg.Scale)
	opts.Precision = cfg.Precision
	opts.Cols = resolveCols(opts.Cols, cfg.Cols)

	m, err := tui.New(cfg.Targets, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	tui.InstallReloadSignal(p)

	_, err = p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
