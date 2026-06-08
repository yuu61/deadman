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
	// fail loudly otherwise, matching the original RuntimeError.
	if s.Source != "" && s.OSName != "Linux" && s.OSName != "Darwin" {
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

func (p *subprocessPinger) buildArgs() []string {
	var cmd []string
	switch p.mode {
	case modeNetns:
		cmd = []string{"ip", "netns", "exec", p.relay["relay"]}
	case modeVRF:
		cmd = []string{"ip", "vrf", "exec", p.relay["relay"]}
	case modeSSH:
		cmd = []string{"ssh", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=no"}
		if k := p.relay["key"]; k != "" {
			cmd = append(cmd, "-i", k)
		}
		if u := p.relay["user"]; u != "" {
			cmd = append(cmd, "-l", u)
		}
		cmd = append(cmd, p.relay["relay"])
	}

	cmd = append(cmd, pingCommand(ipVersion(p.addr))...)
	if p.source != "" {
		switch p.osname {
		case "Linux":
			cmd = append(cmd, "-I", p.source)
		case "Darwin":
			cmd = append(cmd, "-S", p.source)
		}
	}
	return append(cmd, p.addr)
}

func (p *subprocessPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, subprocessTimeout)
	defer cancel()

	args := p.buildArgs()
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
		// Distinguish the result glyphs the original intended but never reached:
		// connect timeout -> t, other ssh-level failure (exit 255) -> s, a remote
		// ping that simply got no reply -> X.
		if ctx.Err() == context.DeadlineExceeded {
			return Result{Code: SSHTimeout, TTL: -1}
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 255 {
			lower := strings.ToLower(stderr.String())
			if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
				return Result{Code: SSHTimeout, TTL: -1}
			}
			return Result{Code: SSHFailed, TTL: -1}
		}
	} else if ctx.Err() == context.DeadlineExceeded {
		return Result{Code: Failed, TTL: -1}
	}

	return ParsePingOutput(stdout.String())
}
