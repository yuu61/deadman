//go:build unix

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"
)

// hangupPoll is how often the terminal is checked for a hang-up no SIGHUP reported.
const hangupPoll = time.Second

// WatchTerminal wires SIGHUP to a config reload (Unix only) by injecting a reloadMsg into
// the running program, and quits the program once its terminal is gone.
//
// A terminal that hangs up (an ssh session dropped, a terminal window closed) leaves no
// one to show anything to, and running on would leave an orphaned monitor probing and
// logging behind the back of a restarted one. The kernel reports a hang-up by SIGHUP to
// the session leader only: deadman sees it when it leads the session or its shell relays
// it, and then quits instead of reloading. A shell that does not relay it (a disowned
// job, a shell killed first) leaves deadman no signal at all, so the terminal is also
// checked every hangupPoll.
// Pass the same lifetime context to tea.WithContext and cancel it when Run returns.
// The returned channel closes after the ticker and signal subscription are released.
func WatchTerminal(ctx context.Context, program *tea.Program) <-chan error {
	signals := make(chan os.Signal, 1)
	done := make(chan error, 1)

	signal.Notify(signals, syscall.SIGHUP)

	ticker := time.NewTicker(hangupPoll)
	hungUp, release := watchTerminalProbe()

	go func() {
		defer close(done)
		defer signal.Stop(signals)
		defer ticker.Stop()

		defer func() { done <- release() }()

		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				if hungUp() {
					program.Quit()

					return
				}

				program.Send(reloadMsg{})
			case <-ticker.C:
				if hungUp() {
					program.Quit()

					return
				}
			}
		}
	}()

	return done
}

// ttyProbe asks the terminal on fd for its size, which only a live terminal answers.
func ttyProbe(fd int) error {
	_, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return fmt.Errorf("query the terminal size of fd %d: %w", fd, err)
	}

	return nil
}

// terminalHungUp reports whether the terminal the program runs on is gone: a descriptor
// that is not a terminal at all (ENOTTY: redirected) says nothing, nor does an
// interrupted request (EINTR), but one that fails the terminal's own request does (a
// hung-up terminal answers EIO, a revoked one EBADF).
func terminalHungUp(probe func(fd int) error) bool {
	// The descriptors the program draws on and reads keys from: Bubble Tea's default
	// input and output. They are named by number, since os.File.Fd would switch the
	// input the program is reading to blocking mode.
	for _, fd := range [...]int{unix.Stdin, unix.Stdout} {
		err := probe(fd)
		if terminalFailed(err) {
			return true
		}
	}

	return false
}

func terminalFailed(err error) bool {
	return err != nil && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINTR)
}

// Bubble Tea opens /dev/tty for keys when stdin is redirected. Observe that same
// terminal without reading it or changing the input descriptor's mode.
func watchTerminalProbe() (func() bool, func() error) {
	tty, err := unix.Open("/dev/tty", unix.O_RDONLY|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return func() bool { return terminalHungUp(ttyProbe) }, func() error { return nil }
	}

	return func() bool {
			return terminalHungUp(ttyProbe) || terminalFailed(ttyProbe(tty))
		}, func() error {
			closeErr := unix.Close(tty)
			if closeErr != nil {
				return fmt.Errorf("close controlling terminal: %w", closeErr)
			}

			return nil
		}
}
