// Command deadman is a cross-platform (Windows/Linux/macOS) TUI host-status
// monitor that probes hosts with ICMP echo (or a relay) and renders their
// reachability, loss, and RTT as a live result bar.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/infrastructure/configfile"
	"github.com/yuu61/deadman/internal/infrastructure/hostinfo"
	"github.com/yuu61/deadman/internal/infrastructure/logfile"
	"github.com/yuu61/deadman/internal/infrastructure/prober"
	"github.com/yuu61/deadman/internal/infrastructure/termfont"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
	"github.com/yuu61/deadman/internal/presentation/tui"
)

// version is the build version shown in the TUI title bar. It is overridden at build
// time via -ldflags "-X main.version=..." (see the Makefile, which derives it from git
// describe); a plain `go install`/`go build` leaves it at "dev".
var version = "dev"

// glyphFlag registers -g/--glyph on fs and returns where the parsed value lands. The
// value is validated as it is parsed, so a typo is a usage error rather than a silent
// fallback; "" (unset) lets the config directive or auto decide.
func glyphFlag(fs *flag.FlagSet) *string {
	var glyph string

	// The backquoted word names the value in -h ("-g set").
	usage := "RESULT bar glyph `set`: " + tui.GlyphChoices() + " (default " + tui.GlyphAuto + ")"
	set := func(s string) error {
		err := tui.CheckGlyph(s)
		if err == nil {
			glyph = s
		}

		return err
	}

	fs.Func("g", usage, set)
	fs.Func("glyph", usage, set)

	return &glyph
}

// cliArgs is the parsed command line: what to monitor and log (for the composition
// root), how to probe, and the display flags (resolved by the TUI against the config).
type cliArgs struct {
	ConfigPath string
	LogDir     string
	Async      bool
	Blink      bool
	Display    tui.Flags
}

// parseArgs parses the command line. Flags may appear before or after the configfile;
// Go's flag package stops at the first non-flag argument, so we collect positionals and
// re-parse the remainder to let flags and the configfile intermix.
func parseArgs(args []string) (cliArgs, error) {
	fs := flag.NewFlagSet("deadman", flag.ContinueOnError)
	scaleUsage := fmt.Sprintf(
		"scale of ping RTT bar gap (ms, default %g, decimals allowed)",
		resultbar.DefaultScale,
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
	glyph := glyphFlag(fs)

	var positional []string

	rest := args
	for {
		err := fs.Parse(rest)
		if err != nil {
			return cliArgs{}, err
		}

		rest = fs.Args()
		if len(rest) == 0 {
			break
		}

		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	if len(positional) < 1 {
		return cliArgs{}, errors.New("configfile is required")
	}
	// Exactly one configfile is accepted. Erroring on extras (rather than silently
	// using the first) also closes a `--` foot-gun: `deadman -- -a cfg.conf` previously
	// dropped cfg.conf and ran the nonexistent file "-a".
	if len(positional) > 1 {
		return cliArgs{}, fmt.Errorf(
			"only one configfile may be given, got %d: %v",
			len(positional), positional,
		)
	}

	return cliArgs{
		ConfigPath: positional[0],
		LogDir:     *logdir,
		Async:      *async,
		Blink:      *blink,
		Display: tui.Flags{
			Scale:    *scale,
			ScaleSet: flagGiven(fs, "s", "scale"),
			Glyph:    *glyph,
			Cols:     *cols,
			ColsSet:  flagGiven(fs, "c", "split"),
		},
	}, nil
}

// flagGiven reports whether a flag under any of names was actually given on the command
// line. It distinguishes an explicit `-s 0` or `-c 0` (a rejected value the TUI warns
// about) from an unset flag — the parsed value cannot, since both leave it at the flag's
// 0 default — by asking the flag set which flags were set.
func flagGiven(fs *flag.FlagSet, names ...string) bool {
	given := false

	fs.Visit(func(f *flag.Flag) {
		given = given || slices.Contains(names, f.Name)
	})

	return given
}

// newService wires the application service to the real adapters (see newPorts). The
// returned LogWriter (nil without -l) must be closed once monitoring has stopped, to flush
// its queued lines.
func newService(configPath, logDir string) (*monitoring.Service, *logfile.LogWriter) {
	ports, logWriter := newPorts(configPath, logDir)

	return monitoring.NewService(ports), logWriter
}

// newPorts binds the application's ports to the real adapters: the probing modes, the
// config file at configPath (read at start and reread by a reload), this host's probing
// capabilities, and — with -l — the per-probe log files, written by the returned
// LogWriter. The session waits in real time.
func newPorts(configPath, logDir string) (monitoring.Ports, *logfile.LogWriter) {
	ports := monitoring.Ports{
		NewPinger:  prober.New,
		LoadConfig: func(ctx context.Context) (config.Config, error) { return configfile.Load(ctx, configPath) },
		Host:       prober.Host{},
	}

	var logWriter *logfile.LogWriter

	// Set Log only with a writer: a nil *LogWriter stored in the interface would not be
	// a nil ResultLog, and recording a result would call it.
	if logDir != "" {
		logWriter = logfile.NewLogWriter(logDir)
		ports.Log = logWriter
	}

	return ports, logWriter
}

// closeLog drains the queued log lines, if logging, reporting a write error. Monitoring
// has stopped, so no Log call races this Close.
func closeLog(w *logfile.LogWriter) error {
	if w == nil {
		return nil
	}

	return w.Close()
}

// run starts the TUI on m and blocks until it exits.
func run(m tui.Model) error {
	return runWithOptions(context.Background(), m)
}

// runWithOptions accepts program options for a controlled input/output surface.
func runWithOptions(parent context.Context, m tui.Model, options ...tea.ProgramOption) error {
	defer m.Close()
	// Ask the terminal for its background now, while nothing else reads stdin.
	tui.DetectBackground()

	ctx, cancel := context.WithCancel(parent)
	options = append([]tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}, options...)
	p := tea.NewProgram(m, options...)
	done := tui.WatchTerminal(ctx, p)

	_, err := p.Run()

	cancel()

	terminalErr := <-done

	if err != nil {
		err = fmt.Errorf("run the terminal UI: %w", err)
	}

	return errors.Join(err, terminalErr)
}

// execute runs the command and returns its exit status after all cleanup.
func execute(
	argv []string,
	service func(string, string) (*monitoring.Service, *logfile.LogWriter),
	runUI func(tui.Model) error,
) int {
	args, err := parseArgs(argv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h/--help: the flag package already printed usage; exit success like
			// flag.ExitOnError would, rather than reporting "flag: help requested".
			return 0
		}

		fmt.Fprintln(os.Stderr, "usage: deadman [options] configfile")
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	svc, logWriter := service(args.ConfigPath, args.LogDir)

	session, loaded, err := svc.Open(context.Background(), args.Async)
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Join(err, closeLog(logWriter)))

		return 1
	}

	host := hostinfo.Lookup(context.Background())
	m := tui.New(session, loaded, tui.Options{
		Hostname:    host.Name,
		HostAddress: host.Address,
		Version:     version,
		Blink:       args.Blink,
		Display:     args.Display,
		// Bubble Tea draws on stdout, so that is the terminal whose font auto inspects.
		BlockGlyphsOK: func() bool {
			return termfont.CanRender(os.Stdout, resultbar.BarBlock.Chars())
		},
	})

	err = runUI(m)

	// os.Exit skips defers, so drain the log explicitly before the error exit too.
	err = errors.Join(err, closeLog(logWriter))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	return 0
}

func main() {
	os.Exit(execute(os.Args[1:], newService, run))
}
