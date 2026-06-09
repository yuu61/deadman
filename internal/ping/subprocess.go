package ping

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// subprocessMode selects how the remote `ping -c 1` is wrapped.
type subprocessMode int

const (
	modeSSH subprocessMode = iota
	modeNetns
	modeVRF
)

const subprocessTimeout = 5 * time.Second

// sshExitCodeError is the exit status ssh uses for its own connection-level
// failures (as opposed to the remote command's exit code).
const sshExitCodeError = 255

// subprocessPinger runs `ping -c 1` on a remote host (SSH) or inside a Linux
// network namespace / VRF, and parses the resulting output. When the underlying
// binary (ssh/ip) is absent — e.g. on Windows — the probe fails gracefully as X.
type subprocessPinger struct {
	addr   string
	osname string
	source string
	relay  map[string]string
	mode   subprocessMode
}

func newSubprocessPinger(s Spec, mode subprocessMode) (Pinger, error) {
	if s.Relay["relay"] == "" {
		return nil, fmt.Errorf("'relay' is not specified for %s", s.Addr)
	}
	// The remote `ping` only takes a source flag on Linux (-I) / Darwin (-S);
	// reject a source on any other OS.
	if s.Source != "" && s.OSName != osLinux && s.OSName != osDarwin {
		return nil, fmt.Errorf("'source' not supported on %s", s.OSName)
	}

	return &subprocessPinger{
		addr:   s.Addr,
		osname: s.OSName,
		source: s.Source,
		relay:  s.Relay,
		mode:   mode,
	}, nil
}

func (p *subprocessPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, subprocessTimeout)
	defer cancel()

	args := p.buildArgs(ctx)
	// #nosec G204 -- relay/source come from the trusted local config file;
	// running the remote `ping` via ssh/ip is this relay mode's purpose.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	cmd.Env = append(os.Environ(), "LC_ALL=C")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		// e.g. ssh/ip not installed on this OS.
		return Result{Code: Failed, TTL: -1}
	}

	if p.mode == modeSSH {
		if res, ok := sshFailure(ctx, err, stderr.String()); ok {
			return res
		}
	} else if ctx.Err() == context.DeadlineExceeded {
		return Result{Code: Failed, TTL: -1}
	}

	return ParsePingOutput(stdout.String())
}

// sshFailure classifies an ssh-level failure into a result glyph: connect timeout
// -> t, other ssh-level failure (exit 255) -> s. ok is false when this was not an
// ssh-level failure, in which case the caller falls back to parsing the remote
// ping output (no reply -> X).
func sshFailure(ctx context.Context, err error, stderr string) (Result, bool) {
	if ctx.Err() == context.DeadlineExceeded {
		return Result{Code: SSHTimeout, TTL: -1}, true
	}

	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == sshExitCodeError {
		lower := strings.ToLower(stderr)
		if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
			return Result{Code: SSHTimeout, TTL: -1}, true
		}

		return Result{Code: SSHFailed, TTL: -1}, true
	}

	return Result{}, false
}

func (p *subprocessPinger) buildArgs(ctx context.Context) []string {
	var cmd []string

	switch p.mode {
	case modeNetns:
		cmd = []string{"ip", viaNetns, "exec", p.relay["relay"]}
	case modeVRF:
		cmd = []string{"ip", viaVRF, "exec", p.relay["relay"]}
	case modeSSH:
		cmd = p.sshArgs()
	default:
		// no wrapper: run ping directly.
	}

	cmd = append(cmd, pingCommand(ipVersion(ctx, p.addr))...)
	cmd = append(cmd, p.sourceArgs()...)

	return append(cmd, p.addr)
}

// sshArgs builds the `ssh` wrapper argv (key/user options then the relay host).
func (p *subprocessPinger) sshArgs() []string {
	cmd := []string{"ssh", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=no"}
	if k := p.relay["key"]; k != "" {
		cmd = append(cmd, "-i", k)
	}

	if u := p.relay["user"]; u != "" {
		cmd = append(cmd, "-l", u)
	}

	return append(cmd, p.relay["relay"])
}

// sourceArgs returns the remote ping's source-interface flag (-I on Linux, -S on
// Darwin), or nil when no source is set. Other OSes are rejected at construction.
func (p *subprocessPinger) sourceArgs() []string {
	if p.source == "" {
		return nil
	}

	switch p.osname {
	case osLinux:
		return []string{"-I", p.source}
	case osDarwin:
		return []string{"-S", p.source}
	default:
		return nil
	}
}
