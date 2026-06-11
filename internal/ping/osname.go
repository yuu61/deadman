package ping

import (
	"context"
	"net"
	"runtime"
)

// Canonical OS names, as reported by `uname -s`.
const (
	osLinux   = "Linux"
	osDarwin  = "Darwin"
	osFreeBSD = "FreeBSD"
	osWindows = "Windows"
)

// IP protocol versions.
const (
	ipv4 = 4
	ipv6 = 6
)

// cmdPing / cmdPing6 are the ping binaries the subprocess relay modes invoke.
const (
	cmdPing  = "ping"
	cmdPing6 = "ping6"
)

// DefaultOSName maps the host GOOS to its canonical `uname -s` name. It controls
// the source-interface flag (-I/-S) and the remote ping command used by the
// subprocess relay modes.
func DefaultOSName() string {
	switch runtime.GOOS {
	case "linux":
		return osLinux
	case "darwin":
		return osDarwin
	case "freebsd":
		return osFreeBSD
	case "windows":
		return osWindows
	default:
		return runtime.GOOS
	}
}

// ipVersion returns 4 or 6 for addr (resolving names if needed), or 0 if unknown.
func ipVersion(ctx context.Context, addr string) int {
	ip := net.ParseIP(addr)
	if ip == nil {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, addr)
		if err != nil || len(ips) == 0 {
			return 0
		}

		ip = ips[0].IP
	}

	if ip.To4() != nil {
		return ipv4
	}

	return ipv6
}

// pingCommand builds the base ping argv for the subprocess relay modes. IPv6 is the
// fiddly case and is chosen by the target's OS (for ssh that is the relay's os=
// attribute, which the operator sets per target — the same input sourceArgs uses to
// pick -I vs -S), not by probing a local ping6 for a command that runs on the
// possibly-different remote host:
//
//   - macOS keeps a separate ping6 and its `ping` does not accept IPv6, so Darwin
//     must use ping6.
//   - modern Linux/FreeBSD fold IPv6 into a unified `ping -6` and may not ship a
//     separate ping6 binary at all, so everything else uses `ping -6`.
//
// The earlier local exec.LookPath("ping6") was wrong for ssh (the local binary set
// is irrelevant to the remote) and the "always ping6" interim broke remotes that
// only have the unified ping.
func pingCommand(ipv int, osname string) []string {
	switch {
	case ipv != ipv6:
		return []string{cmdPing, "-c", "1"}
	case osname == osDarwin:
		return []string{cmdPing6, "-c", "1"}
	default:
		return []string{cmdPing, "-6", "-c", "1"}
	}
}
