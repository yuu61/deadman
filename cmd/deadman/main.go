// Command deadman is a TUI host-status monitor using ping. It is a Go rewrite of
// the original Python tool, keeping the config-file format and CLI flags
// compatible while adding cross-platform (Windows/Linux/macOS) support.
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

// parseArgs parses the command line into TUI options. Flags may appear before or
// after the configfile (the original argparse intermixed them freely), which Go's
// flag package does not do on its own; we collect positionals while re-parsing the
// remainder.
func parseArgs(args []string) (tui.Options, error) {
	fs := flag.NewFlagSet("deadman", flag.ContinueOnError)
	scale := fs.Int("s", defaultScale, "scale of ping RTT bar gap, default 10 (ms)")
	fs.IntVar(scale, "scale", defaultScale, "scale of ping RTT bar gap, default 10 (ms)")
	async := fs.Bool("a", false, "send ping asynchronously")
	fs.BoolVar(async, "async-mode", false, "send ping asynchronously")
	blink := fs.Bool("b", false, "blink arrow in async mode")
	fs.BoolVar(blink, "blink-arrow", false, "blink arrow in async mode")
	logdir := fs.String("l", "", "directory for log files")
	fs.StringVar(logdir, "logging", "", "directory for log files")

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
		ConfigPath: positional[0],
	}, nil
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
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
