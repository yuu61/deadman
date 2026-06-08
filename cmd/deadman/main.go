// Command deadman is a TUI host-status monitor using ping. It is a Go rewrite of
// the original Python tool, keeping the config-file format and CLI flags
// compatible while adding cross-platform (Windows/Linux/macOS) support.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/config"
	"github.com/yuu61/deadman/internal/tui"
)

func main() {
	scale := flag.Int("s", 10, "scale of ping RTT bar gap, default 10 (ms)")
	flag.IntVar(scale, "scale", 10, "scale of ping RTT bar gap, default 10 (ms)")
	async := flag.Bool("a", false, "send ping asynchronously")
	flag.BoolVar(async, "async-mode", false, "send ping asynchronously")
	blink := flag.Bool("b", false, "blink arrow in async mode")
	flag.BoolVar(blink, "blink-arrow", false, "blink arrow in async mode")
	logdir := flag.String("l", "", "directory for log files")
	flag.StringVar(logdir, "logging", "", "directory for log files")
	flag.Parse()

	// Go's flag package stops at the first non-flag argument; collect positionals
	// while re-parsing the remainder so flags may follow the configfile (the
	// original argparse intermixed options and the positional freely).
	var positional []string
	for rest := flag.Args(); len(rest) > 0; rest = flag.Args() {
		positional = append(positional, rest[0])
		flag.CommandLine.Parse(rest[1:])
	}
	if len(positional) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deadman [options] configfile")
		flag.PrintDefaults()
		os.Exit(2)
	}

	f, err := os.Open(positional[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	specs, err := config.ParseConfig(f)
	f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	m, err := tui.New(specs, tui.Options{
		Async:      *async,
		Blink:      *blink,
		Scale:      *scale,
		LogDir:     *logdir,
		ConfigPath: positional[0],
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	tui.InstallReloadSignal(p)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
