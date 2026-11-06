package prober

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

const subprocessTimeout = 5 * time.Second

// sshExitCodeError is the exit status ssh uses for its own connection-level
// failures (as opposed to the remote command's exit code).
const sshExitCodeError = 255

// cmdPing / cmdPing6 are the ping binaries the subprocess relay modes invoke.
const (
	cmdPing  = "ping"
	cmdPing6 = "ping6"
)

// reTimeFloat and reTimeInt match the RTT of a ping reply line: time=<float|int>.
var (
	reTimeFloat = regexp.MustCompile(`time=(\d+\.\d+)`)
	reTimeInt   = regexp.MustCompile(`time=(\d+)`)
	reNoReply   = regexp.MustCompile(
		`(?m)\b[1-9]\d* packets? transmitted, 0 (?:packets? )?received\b`,
	)
	rePingError = regexp.MustCompile(
		`(?i)message too long|packet too (?:big|large)|frag(?:mentation)? needed|cannot fragment`,
	)
)

// subprocessPinger runs `ping -c 1` on a remote host (SSH) or inside a Linux
// network namespace / VRF, and parses the resulting output. When the underlying
// binary (ssh/ip) is absent — e.g. on Windows — the probe fails gracefully as X.
type subprocessPinger struct {
	addr     string
	named    bool         // addr is a host name, which the ping resolves there.
	wrapper  []string     // the argv that runs the ping there: ssh … HOST, or ip netns|vrf exec NAME.
	os       probe.OS     // the ping's dialect.
	family   probe.Family // the family the ping runs in, fixed by the plan.
	source   probe.Source
	echo     probe.EchoOptions
	iface    probe.CLIWord // JUNOS egress interface.
	instance probe.CLIWord // JUNOS routing instance.
	timeout  time.Duration
	// ssh: the ping's argv reaches the relay's shell as one command line, and an
	// ssh-level failure (exit 255) is the relay's, not the target's.
	ssh bool
}

func newSSHPinger(dest probe.Destination, relay probe.SSH) (probe.Pinger, error) {
	// The ssh host and the remote ping's destination are bare operands in the argv, so a
	// leading '-' would be parsed as an option — most dangerously a relay of
	// "-oProxyCommand=..." that runs an arbitrary local command. Reject both.
	addr := dest.String()

	err := validateOperands(relay.Host, addr)
	if err != nil {
		return nil, err
	}

	_, named := dest.Name()

	return &subprocessPinger{
		addr:     addr,
		named:    named,
		wrapper:  sshArgs(relay),
		os:       relay.OS,
		family:   relay.Family,
		source:   relay.Source,
		echo:     relay.Echo,
		iface:    relay.Interface,
		instance: relay.Instance,
		timeout:  relay.Timeout.Duration(),
		ssh:      true,
	}, nil
}

func newNetnsPinger(dest probe.Destination, n probe.Netns) (probe.Pinger, error) {
	return newNamespacePinger(dest, probe.Namespace(n), "netns")
}

func newVRFPinger(dest probe.Destination, v probe.VRF) (probe.Pinger, error) {
	return newNamespacePinger(dest, probe.Namespace(v), "vrf")
}

// newNamespacePinger runs the ping through `ip netns|vrf exec NAME`, whose ping is this
// Linux host's.
func newNamespacePinger(
	dest probe.Destination,
	n probe.Namespace,
	kind string,
) (probe.Pinger, error) {
	// The namespace or VRF name and addr are bare operands in the argv; reject a leading '-'.
	addr := dest.String()

	err := validateOperands(n.Name, addr)
	if err != nil {
		return nil, err
	}

	_, named := dest.Name()

	return &subprocessPinger{
		addr:    addr,
		named:   named,
		wrapper: []string{"ip", kind, "exec", n.Name},
		os:      probe.OSLinux,
		family:  n.Family,
		source:  n.Source,
		echo:    n.Echo,
		timeout: subprocessTimeout,
	}, nil
}

func (p *subprocessPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	out, err := runCapped(ctx, p.buildArgs(), append(os.Environ(), "LC_ALL=C"))
	if errors.Is(err, exec.ErrNotFound) {
		// e.g. ssh/ip not installed on this OS.
		return probe.UnavailableResult()
	}

	if p.ssh {
		if res, ok := sshFailure(ctx, err, out.stderr); ok {
			return res
		}
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return probe.UnavailableResult()
	}

	// A wrapper may fail with the same status as an unanswered ping, so the output
	// parser also requires evidence that the command sent an echo.
	if !pingExitStatus(p.os, err) {
		return probe.UnavailableResult()
	}

	return parsePingResult(out)
}

// pingExitStatus accepts successful exits and the OS's no-reply exit code.
// Linux uses 1; Darwin and FreeBSD use 2. Other failures measured no target.
func pingExitStatus(pingOS probe.OS, err error) bool {
	if err == nil {
		return true
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	if pingOS == probe.OSLinux {
		return exitErr.ExitCode() == 1
	}

	if pingOS == probe.OSJUNOS && exitErr.ExitCode() == 1 {
		return true
	}

	return exitErr.ExitCode() == 2
}

// sshFailure classifies an ssh-level failure into a result glyph: connect timeout
// -> t, other ssh-level failure (exit 255) -> s. ok is false when this was not an
// ssh-level failure, in which case the caller falls back to parsing the remote
// ping output (no reply -> X).
func sshFailure(ctx context.Context, err error, stderr string) (probe.Result, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return probe.RelayTimeoutResult(), true
	}

	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == sshExitCodeError {
		lower := strings.ToLower(stderr)
		if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
			return probe.RelayTimeoutResult(), true
		}

		return probe.RelayFailedResult(), true
	}

	return probe.Result{}, false
}

// buildArgs is the argv that runs the ping. Through ssh the ping's argv reaches the relay
// as one command line its shell splits again, so each word is quoted for that shell: a
// name or source is then one word there, never a glob, an expansion or another command.
func (p *subprocessPinger) buildArgs() []string {
	if p.os == probe.OSJUNOS {
		return append(slices.Clone(p.wrapper), p.junosCommand()...)
	}

	ping := pingCommand(p.family, p.os, p.named)
	ping = append(ping, p.sourceArgs()...)
	ping = append(ping, p.echoArgs()...)
	ping = append(ping, p.addr)

	if p.ssh {
		for i, word := range ping {
			ping[i] = shellQuote(word)
		}

		// sshd need not accept the local client's locale environment.
		ping = append([]string{"env", "LC_ALL=C"}, ping...)
	}

	return append(slices.Clone(p.wrapper), ping...)
}

// shellQuote quotes word for a POSIX shell, leaving a word of only safe characters as it
// is so the common command line stays readable.
func shellQuote(word string) string {
	if word != "" && strings.IndexFunc(word, shellSpecial) < 0 {
		return word
	}

	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// shellSpecial reports whether a POSIX shell might read r as anything but a word's
// character.
func shellSpecial(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return false
	default:
		return !strings.ContainsRune("-_.,:/%@+=", r)
	}
}

// sshArgs builds the `ssh` wrapper argv (key/user options then the relay host).
//
// BatchMode keeps ssh from prompting: it would ask for a password or a key's passphrase
// on /dev/tty, the terminal the TUI draws on and reads keys from, every round. A relay
// needing one fails as s instead. accept-new trusts a relay's key on first use, as
// unattended monitoring needs, but refuses one whose key has changed.
func sshArgs(s probe.SSH) []string {
	cmd := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if s.Key != "" {
		cmd = append(cmd, "-i", s.Key)
	}

	if s.User != "" {
		cmd = append(cmd, "-l", s.User)
	}

	return append(cmd, s.Host)
}

// sourceFlags maps each relay OS whose remote `ping` takes a source as a flag to that flag:
// -I on Linux (an address or an interface), -S on Darwin (an address). JUNOS takes a
// source as a CLI keyword instead (junosCommand). Which sources each dialect takes is
// Compile's rule, which a plan has passed; this is only their spelling (a test checks that
// every dialect Compile gives a source has one).
var sourceFlags = map[probe.OS]string{probe.OSLinux: "-I", probe.OSDarwin: "-S"}

// sourceArgs returns the remote ping's source flag and value, or nil when no source is
// set. The value is the source as written: an address keeps its zone.
func (p *subprocessPinger) sourceArgs() []string {
	if !p.source.IsSet() {
		return nil
	}

	return []string{sourceFlags[p.os], p.source.String()}
}

// pingCommand builds the base ping argv for the subprocess relay methods, chosen by
// pingOS, the OS the ping runs on (for ssh that is the relay's os= attribute, which the
// operator sets per target — the same input sourceArgs uses to pick -I vs -S), not by
// probing a local ping6 for a command that runs on the possibly-different remote host:
//
//   - Linux folds IPv6 into a unified `ping -6` and may not ship a separate ping6.
//   - FreeBSD (13 and later) does the same, its -W waiting in milliseconds for both.
//   - macOS keeps a separate ping6, and its `ping` does not accept IPv6.
//
// A name gets -4 for IPv4 where the ping would otherwise resolve it in the family it
// finds first (Linux and FreeBSD), so resolve_family holds; macOS's ping is IPv4 only. An
// IPv4 literal needs no -4, which leaves it to relays whose ping predates the flag.
func pingCommand(family probe.Family, pingOS probe.OS, named bool) []string {
	switch {
	case pingOS == probe.OSDarwin && family == probe.FamilyIPv6:
		return []string{cmdPing6, "-c", "1"}
	case pingOS == probe.OSDarwin:
		return []string{cmdPing, "-c", "1", "-W", "1000"}
	}

	wait := "1"
	if pingOS == probe.OSFreeBSD {
		wait = "1000"
	}

	cmd := []string{cmdPing}

	switch {
	case family == probe.FamilyIPv6:
		cmd = append(cmd, "-6")
	case named:
		cmd = append(cmd, "-4")
	default:
		// An IPv4 literal: the ping takes its family from it.
	}

	return append(cmd, "-c", "1", "-W", wait)
}

// parsePingResult also reads stderr: a send failure is not an unanswered echo, even
// when the ping summary counts the attempted send as a transmitted packet.
func parsePingResult(out output) probe.Result {
	result := parsePingOutput(out.stdout)
	if result.Code == probe.Failed && rePingError.MatchString(out.stderr) {
		return probe.UnavailableResult()
	}

	return result
}

// parsePingOutput recognizes a reply's RTT, or ping's summary of an echo that got no reply.
// A reply with no RTT is not a measurement: Compile keeps payloads too small to hold ping's
// timestamp out, so this is output the parser does not know, and it is unavailable.
func parsePingOutput(out string) probe.Result {
	var rtt float64

	matched := false

	if fm := reTimeFloat.FindStringSubmatch(out); fm != nil {
		rtt, matched = parseRTT(fm[1])
	} else if im := reTimeInt.FindStringSubmatch(out); im != nil {
		rtt, matched = parseRTT(im[1])
	}

	if matched {
		return probe.SuccessResult(rtt)
	}

	if reNoReply.MatchString(out) && !rePingError.MatchString(out) {
		return probe.FailedResult()
	}

	return probe.UnavailableResult()
}

// parseRTT reads a reply's RTT in milliseconds. A number too large for a float (a relay
// printing hundreds of digits) is no RTT a statistic can take, so it is not one.
func parseRTT(s string) (float64, bool) {
	rtt, err := strconv.ParseFloat(s, 64)

	return rtt, err == nil && !math.IsInf(rtt, 0)
}
