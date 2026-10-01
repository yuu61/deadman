package prober

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// subprocessWaitDelay bounds how long a probe's command may keep its output pipes open
// after it exited or was killed at the probe's deadline. A process it started (an ssh
// ProxyCommand, say) inherits the pipes and may outlive it indefinitely; waiting for its
// EOF would stall the probe, and with it every later probe of the round.
const subprocessWaitDelay = time.Second

// output is what a probe's command wrote, each stream capped at maxProbeOutput.
type output struct{ stdout, stderr string }

// runCapped runs argv under ctx and returns its capped output and its error. A command
// that exited successfully while something it started still held the pipes is a
// success: its own output is complete.
func runCapped(ctx context.Context, argv, env []string) (output, error) {
	// #nosec G204 -- argv is built from the trusted local config by each probing mode;
	// running that relay or tool is the mode's purpose.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = subprocessWaitDelay

	// Separate capped writers: the ssh classification reads stderr apart from the ping's
	// stdout, and the cap bounds memory if a hostile relay floods either stream.
	stdout := &capWriter{limit: maxProbeOutput}
	stderr := &capWriter{limit: maxProbeOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := runProbeCommand(cmd)

	return output{stdout: stdout.String(), stderr: stderr.String()}, err
}

// Normalize only the successful command's pipe timeout, before joining any cleanup
// failure. Cleanup failures must still be reported even when helpers held the pipes.
func waitProbeCommand(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("wait for probe command: %w", err)
	}

	return nil
}

// maxProbeOutput caps how many bytes of a subprocess's stdout/stderr we buffer per
// probe. A few KB holds any legitimate ping/hping summary; capping keeps a
// hostile or compromised relay/target (e.g. an ssh relay whose key was accepted on
// first use) from streaming unbounded output into memory. With one
// probe goroutine per target and no global concurrency cap, an uncapped flood across
// many targets could otherwise exhaust the monitor.
const maxProbeOutput = 64 << 10 // 64 KiB.

// capWriter buffers at most limit bytes and silently discards the rest, reporting
// every write as fully accepted so the child process is never blocked or errored. It
// is written by a single exec goroutine per Send, so it needs no locking.
type capWriter struct {
	buf   strings.Builder
	limit int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if rem := w.limit - w.buf.Len(); rem > 0 {
		if len(p) > rem {
			w.buf.Write(p[:rem])
		} else {
			w.buf.Write(p)
		}
	}

	// Claim the whole slice as written so exec keeps draining the pipe (and the child
	// is neither blocked nor killed with a short-write error); the excess is dropped.
	return len(p), nil
}

func (w *capWriter) String() string { return w.buf.String() }

// validateOperands returns an error for the first value that begins with '-'. These
// values are placed as BARE operands in a subprocess argv — an ssh relay host, an
// `ip netns/vrf` namespace name, or a ping/hping destination address — so the spawned
// tool's getopt would parse a leading-'-' token as an option rather than an operand
// (argument injection). The dangerous case is an ssh relay of "-oProxyCommand=..." which
// makes ssh run an arbitrary local command at deadman's privilege; the milder cases
// redirect the probe into an unintended flag. A legitimate host/address/namespace never
// begins with '-'. Values consumed as a PRECEDING flag's argument (community after -c,
// user after -l, key after -i, source after -I/-S) are not operands and are not passed
// here. New surfaces the error so the target degrades to a permanent failure glyph
// with a startup warning rather than aborting the program.
func validateOperands(values ...string) error {
	for _, v := range values {
		if strings.HasPrefix(v, "-") {
			return fmt.Errorf(
				"operand %q must not start with '-' (it would be parsed as a command-line option)",
				v,
			)
		}
	}

	return nil
}
