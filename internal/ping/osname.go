package ping

import (
	"context"
	"net"
	"os/exec"
	"runtime"
)

// Canonical OS names, matching what the original obtained from `uname -s`.
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

// DefaultOSName maps the host GOOS to the OS name the original obtained from
// `uname -s`. It controls the source-interface flag (-I/-S) and the remote ping
// command used by the subprocess relay modes.
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

// pingCommand builds the base ping argv for the subprocess relay modes. As in the
// original, ping6 is preferred for IPv6 when available locally.
func pingCommand(ipv int) []string {
	if ipv == ipv6 {
		_, err := exec.LookPath("ping6")
		if err == nil {
			return []string{"ping6", "-c", "1"}
		}
	}

	return []string{"ping", "-c", "1"}
}
