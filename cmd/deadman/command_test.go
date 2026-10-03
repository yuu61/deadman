package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/probe"
	"github.com/yuu61/deadman/internal/infrastructure/logfile"
	"github.com/yuu61/deadman/internal/presentation/tui"
)

// Run the real main in a child: parseArgs returning ErrHelp alone does not prove
// that the executable exits successfully, or that usage and startup errors differ.
func TestCLIExitStatus(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		args []string
		code int
		text string
	}{
		{"help", []string{"--help"}, 0, "Usage of deadman"},
		{"missing_argument", nil, 2, "configfile is required"},
		{"unknown_flag", []string{"--unknown"}, 2, "flag provided but not defined"},
		{"missing_config", []string{filepath.Join(t.TempDir(), "missing.conf")}, 1, "missing.conf"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			args := append([]string{"-test.run=^TestCLIProcess$", "--"}, c.args...)
			cmd := exec.CommandContext(ctx, executable, args...)

			cmd.Env = append(os.Environ(), "DEADMAN_TEST_MAIN=1")
			output, err := cmd.CombinedOutput()

			code := 0

			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("start CLI: %v", err)
				}

				code = exitErr.ExitCode()
			}

			if code != c.code || !strings.Contains(string(output), c.text) {
				t.Fatalf(
					"CLI exit=%d, output=%q; want exit=%d and %q",
					code,
					output,
					c.code,
					c.text,
				)
			}
		})
	}
}

func TestCLIProcess(t *testing.T) {
	if os.Getenv("DEADMAN_TEST_MAIN") != "1" {
		return
	}

	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("CLI helper has no argument separator")
	}

	os.Args = append([]string{"deadman"}, os.Args[separator+1:]...)

	main()
}

func TestCommandFlushesLogsOnSuccessAndUIError(t *testing.T) {
	uiErr := errors.New("test UI failure")
	for _, c := range []struct {
		name   string
		uiErr  error
		badLog bool
		code   int
	}{
		{"normal_exit", nil, false, 0},
		{"UI_error", uiErr, false, 1},
		{"storage_error", nil, true, 1},
		{"UI_and_storage_errors", uiErr, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			configPath := writeConfig(t, "local 127.0.0.1 probe=tcp port=53\n")

			logDir := filepath.Join(t.TempDir(), "logs")
			if c.badLog {
				err := os.WriteFile(logDir, []byte("not a directory"), 0o600)
				if err != nil {
					t.Fatal(err)
				}
			}

			const readings = 128

			code, output := captureCommandError(t, func() int {
				return execute(
					[]string{"-a", "-l", logDir, configPath},
					instantCommandService,
					func(m tui.Model) error {
						defer m.Close()

						queueCommandReadings(t, m, readings)

						return c.uiErr
					},
				)
			})
			if code != c.code {
				t.Fatalf("command exit=%d, output=%q; want %d", code, output, c.code)
			}

			if c.uiErr != nil && !strings.Contains(output, c.uiErr.Error()) {
				t.Errorf("UI error was lost: %q", output)
			}

			if c.badLog {
				if !strings.Contains(output, logDir) {
					t.Errorf("log close error was lost: %q", output)
				}

				return
			}

			lines := strings.Split(strings.TrimSpace(string(readOnlyLog(t, logDir))), "\n")
			if len(lines) != readings {
				t.Fatalf("command returned with %d log lines, want all %d", len(lines), readings)
			}

			last := strings.Fields(lines[len(lines)-1])

			const logFields = 6
			if len(last) != logFields || last[2] != "up" ||
				last[len(last)-1] != strconv.Itoa(readings) {
				t.Fatalf("last queued reading was not flushed: %q", lines[len(lines)-1])
			}
		})
	}
}

func TestCommandWithoutLoggingExitsNormally(t *testing.T) {
	code, output := captureCommandError(t, func() int {
		return execute([]string{writeConfig(t, "")}, newService, func(m tui.Model) error {
			m.Close()

			return nil
		})
	})
	if code != 0 || output != "" {
		t.Fatalf("command without logging exit=%d, output=%q", code, output)
	}
}

func TestConfigToolsSkipMonitoring(t *testing.T) {
	for _, mode := range []struct {
		name  string
		flags []string
	}{
		{"check", []string{"--check"}},
		{"format", []string{"--format"}},
		{"write", []string{"--format", "-w"}},
	} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/invalid=%t", mode.name, invalid), func(t *testing.T) {
				input, wantCode := "host 192.0.2.1\n", 0
				if invalid {
					input, wantCode = "host\n", 1
				}

				path := writeConfig(t, input)
				args := append([]string{path}, mode.flags...)

				code, output := captureCommandError(t, func() int {
					original := os.Stdout

					os.Stdout = os.Stderr
					defer func() { os.Stdout = original }()

					return execute(args,
						func(string, string) (*monitoring.Service, *logfile.LogWriter) {
							t.Fatal("configuration command constructed monitoring adapters")

							return nil, nil
						},
						func(tui.Model) error {
							t.Fatal("configuration command started the terminal UI")

							return nil
						},
					)
				})
				if code != wantCode {
					t.Fatalf(
						"configuration command exit=%d, output=%q; want %d",
						code,
						output,
						wantCode,
					)
				}
			})
		}
	}
}

// Only probe replies and round waits are faked. The config, session, model and log
// writer remain real, including their shutdown and error reporting paths.
func instantCommandService(configPath, logDir string) (*monitoring.Service, *logfile.LogWriter) {
	ports, writer := newPorts(configPath, logDir)
	ports.NewPinger = func(probe.Plan, string) (probe.Pinger, error) {
		return fixedPinger{res: probe.SuccessResult(3)}, nil
	}
	ports.Wait = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }

	return monitoring.NewService(ports), writer
}

// Run complete async rounds without wall-clock waits, so flushing is checked on
// a burst of real session results rather than replaying one event.
func queueCommandReadings(t *testing.T, m tui.Model, count int) {
	t.Helper()

	cmd := m.Init()
	for range count {
		m, cmd = commandUpdate(t, m, cmd()) // Start the round.
		m, cmd = commandUpdate(t, m, cmd()) // Apply its probe result.
	}
}

func commandUpdate(t *testing.T, m tui.Model, msg tea.Msg) (tui.Model, tea.Cmd) {
	t.Helper()

	if _, ok := msg.(monitoring.Event); !ok {
		t.Fatalf("command returned %T, want a monitoring event", msg)
	}

	updated, cmd := m.Update(msg)

	model, ok := updated.(tui.Model)
	if !ok || cmd == nil {
		t.Fatalf("monitoring update returned %T with command %v", updated, cmd != nil)
	}

	return model, cmd
}

func captureCommandError(t *testing.T, command func() int) (int, string) {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "stderr-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	original := os.Stderr

	os.Stderr = file
	defer func() { os.Stderr = original }()

	code := command()

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatal(err)
	}

	output, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}

	err = file.Close()
	if err != nil {
		t.Fatal(err)
	}

	return code, string(output)
}

type commandPinger struct {
	started chan struct{}
	stopped chan struct{}
	closed  chan struct{}
}

func (p *commandPinger) Send(ctx context.Context) probe.Result {
	close(p.started)
	<-ctx.Done()
	close(p.stopped)

	return probe.UnavailableResult()
}

func (p *commandPinger) Close() { close(p.closed) }

func TestRunStopsMonitoringOnQuitAndProgramError(t *testing.T) {
	for _, killed := range []bool{false, true} {
		t.Run(fmt.Sprint("killed=", killed), func(t *testing.T) {
			pinger := &commandPinger{
				started: make(chan struct{}),
				stopped: make(chan struct{}),
				closed:  make(chan struct{}),
			}
			ports, _ := newPorts(writeConfig(t, "local 127.0.0.1 probe=tcp port=53\n"), "")
			ports.NewPinger = func(probe.Plan, string) (probe.Pinger, error) { return pinger, nil }

			session, loaded, err := monitoring.NewService(ports).Open(t.Context(), true)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(session.Close)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()

			m := tui.New(session, loaded, tui.Options{})

			done := make(chan error, 1)
			go func() {
				done <- runWithOptions(ctx, m, tea.WithInput(input), tea.WithOutput(io.Discard),
					tea.WithoutRenderer(), tea.WithoutSignalHandler())
			}()

			select {
			case <-pinger.started:
			case <-time.After(3 * time.Second):
				t.Fatal("program did not start the probe")
			}

			if killed {
				cancel()
			} else {
				_, err = io.WriteString(writer, "q")
				if err != nil {
					t.Fatal(err)
				}
			}

			err = writer.Close()
			if err != nil {
				t.Fatal(err)
			}

			select {
			case err = <-done:
				if killed && !errors.Is(err, tea.ErrProgramKilled) {
					t.Fatalf("program error = %v, want ErrProgramKilled", err)
				}

				if !killed && err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("program did not finish shutdown")
			}

			for _, signal := range []<-chan struct{}{pinger.stopped, pinger.closed} {
				select {
				case <-signal:
				default:
					t.Fatal("run returned before monitoring released its resources")
				}
			}
		})
	}
}
