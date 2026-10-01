//go:build !windows

package prober

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func scriptFixture(t *testing.T, script string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "probe.sh")

	err := os.WriteFile(path, []byte(script), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSubprocessNoReplyExitCodes(t *testing.T) {
	tests := []struct {
		name   string
		os     probe.OS
		addr   string
		status int
		output string
		want   probe.ResultCode
	}{
		{
			"linux", probe.OSLinux, "192.0.2.1", 1,
			"1 packets transmitted, 0 received, 100% packet loss", probe.Failed,
		},
		{
			"darwin", probe.OSDarwin, "192.0.2.1", 2,
			"1 packets transmitted, 0 packets received, 100.0% packet loss", probe.Failed,
		},
		{
			"darwin_ipv6", probe.OSDarwin, "2001:db8::1", 2,
			"1 packets transmitted, 0 packets received, 100.0% packet loss", probe.Failed,
		},
		{
			"freebsd", probe.OSFreeBSD, "192.0.2.1", 2,
			"1 packets transmitted, 0 packets received, 100.0% packet loss", probe.Failed,
		},
		{
			"wrong_exit", probe.OSDarwin, "192.0.2.1", 1,
			"1 packets transmitted, 0 packets received, 100.0% packet loss", probe.Unavailable,
		},
		{"no_summary", probe.OSLinux, "192.0.2.1", 1, "", probe.Unavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// ssh hands us its remote command's status; the script stands in for
			// a relay whose ping has finished with the listed output and status.
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\nexit %d\n", tt.output, tt.status)
			path := scriptFixture(t, script)

			plan := compiled(
				t,
				probe.Spec{Addr: tt.addr, Params: probe.SSH{Host: "relay", OS: tt.os}},
			)

			pinger, err := New(plan, "row#1")
			if err != nil {
				t.Fatal(err)
			}

			sp, ok := pinger.(*subprocessPinger)
			if !ok {
				t.Fatalf("unexpected pinger type %T", pinger)
			}

			sp.wrapper = []string{"sh", path}

			result := pinger.Send(t.Context())
			if result.Code != tt.want {
				t.Fatalf("result code = %d, want %d", result.Code, tt.want)
			}

			target := monitor.NewTarget("row", "target", tt.addr)
			target.Consume(result)

			stats := target.Snapshot().Stats
			if tt.want == probe.Failed && (stats.Snt != 1 || stats.Loss != 1) {
				t.Errorf("unanswered echo was not counted as loss: %+v", stats)
			}

			if tt.want == probe.Unavailable && (stats.Snt != 0 || stats.Loss != 0) {
				t.Errorf("unobserved probe was counted as loss: %+v", stats)
			}
		})
	}
}

func TestNamespaceWrapperErrorDoesNotCountAsLoss(t *testing.T) {
	path := scriptFixture(t, "#!/bin/sh\nexit 1\n")

	pinger, err := New(compiled(t, probe.Spec{
		Addr: "192.0.2.1", Params: probe.Netns{Name: "missing"},
	}), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	sp, ok := pinger.(*subprocessPinger)
	if !ok {
		t.Fatalf("unexpected pinger type %T", pinger)
	}

	sp.wrapper = []string{"sh", path}

	if result := pinger.Send(t.Context()); result.Code != probe.Unavailable {
		t.Errorf("wrapper failure = %d, want Unavailable", result.Code)
	}
}

func TestSSHRemoteLocaleWithoutEnvironmentForwarding(t *testing.T) {
	ping := scriptFixture(t, `#!/bin/sh
if [ "${LC_ALL-}" = C ]; then
    echo '1 packets transmitted, 0 received, 100% packet loss'
else
    echo '1 paquetes transmitidos, 0 recibidos, 100% perdida de paquetes'
fi
exit 1
`)
	remote := scriptFixture(t, "#!/bin/sh\nunset LC_ALL\nexec sh -c \"$*\"\n")
	pinger := sshPingerRunning(t, remote)

	sp, ok := pinger.(*subprocessPinger)
	if !ok {
		t.Fatalf("unexpected pinger type %T", pinger)
	}

	// Replace the ping executable with a fixture, preserving the remote command's
	// environment and quoting. The relay drops the client's LC_ALL as sshd may do.
	args := sp.buildArgs()
	command := args[len(sp.wrapper):]

	pingIndex := slices.Index(command, cmdPing)
	if pingIndex < 0 {
		t.Fatalf("remote command has no ping: %v", command)
	}

	command = slices.Replace(command, pingIndex, pingIndex+1, "sh", shellQuote(ping))
	args = append(slices.Clone(sp.wrapper), command...)

	out, err := runCapped(t.Context(), args, append(os.Environ(), "LC_ALL=C"))
	if !pingExitStatus(probe.OSLinux, err) {
		t.Fatalf("remote command failed: %v, stderr=%s", err, out.stderr)
	}

	result := parsePingOutput(out.stdout)
	target := monitor.NewTarget("row", "host", "192.0.2.1")
	target.Consume(result)

	stats := target.Snapshot().Stats
	if result.Code != probe.Failed || stats.Snt != 1 || stats.Loss != 1 {
		t.Fatalf(
			"unanswered remote echo: result=%+v stats=%+v output=%s",
			result,
			stats,
			out.stdout,
		)
	}
}

// holderScript is a relay command that leaves a background process holding its output
// pipes, as an ssh ProxyCommand may; the process's PID goes to pidFile for cleanup.
func holderScript(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "holder.pid")

	t.Cleanup(func() {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return
		}
		defer func() { _ = root.Close() }()

		b, err := root.ReadFile("holder.pid")
		if err != nil {
			return
		}

		var pid int

		_, err = fmt.Sscan(string(b), &pid)
		if err == nil {
			err = syscall.Kill(pid, syscall.SIGKILL)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Logf("kill %d: %v", pid, err)
			}
		}
	})

	return scriptFixture(t, fmt.Sprintf("#!/bin/sh\nsleep 30 &\necho $! > %q\n%s", pidFile, body))
}

func sshPingerRunning(t *testing.T, script string) probe.Pinger {
	t.Helper()

	pinger, err := New(compiled(t, probe.Spec{
		Addr: "192.0.2.1", Params: probe.SSH{Host: "relay", OS: probe.OSLinux},
	}), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	sp, ok := pinger.(*subprocessPinger)
	if !ok {
		t.Fatalf("unexpected pinger type %T", pinger)
	}

	sp.wrapper = []string{"sh", script}

	return pinger
}

// A relay whose ping answered and exited is a success even while something it started
// still holds the output pipes; the probe does not wait for that process.
func TestSubprocessSuccessWithOutputHeldOpen(t *testing.T) {
	script := holderScript(t, "echo '64 bytes from 192.0.2.1: icmp_seq=1 ttl=64 time=0.5 ms'\n"+
		"echo '1 packets transmitted, 1 received, 0% packet loss'\nexit 0\n")

	start := time.Now()
	res := sshPingerRunning(t, script).Send(t.Context())

	if !res.IsSuccess() || res.RTT != 0.5 {
		t.Errorf("result = %+v, want a 0.5 ms success", res)
	}

	if d := time.Since(start); d > subprocessWaitDelay+2*time.Second {
		t.Errorf("Send took %v, waiting for the process holding the pipes", d)
	}
}

// A relay that does not finish by the deadline ends the probe then, even while something
// it started holds the output pipes: one stuck relay must not stall the round.
func TestSubprocessDeadlineWithOutputHeldOpen(t *testing.T) {
	script := holderScript(t, "sleep 30\n")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := sshPingerRunning(t, script).Send(ctx)

	if res.Code != probe.RelayTimeout {
		t.Errorf("result code = %d, want RelayTimeout", res.Code)
	}

	if d := time.Since(start); d > subprocessWaitDelay+2*time.Second {
		t.Errorf("Send took %v past its deadline", d)
	}
}
