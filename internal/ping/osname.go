package ping

import (
	"net"
	"os/exec"
	"runtime"
)

// DefaultOSName maps the host GOOS to the OS name the original obtained from
// `uname -s`. It controls the source-interface flag (-I/-S) and the remote ping
// command used by the subprocess relay modes.
func DefaultOSName() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "darwin":
		return "Darwin"
	case "freebsd":
		return "FreeBSD"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

// ipVersion returns 4 or 6 for addr (resolving names if needed), or 0 if unknown.
func ipVersion(addr string) int {
	ip := net.ParseIP(addr)
	if ip == nil {
		ips, err := net.LookupIP(addr)
		if err != nil || len(ips) == 0 {
			return 0
		}
		ip = ips[0]
	}
	if ip.To4() != nil {
		return 4
	}
	return 6
}

// pingCommand builds the base ping argv for the subprocess relay modes. As in the
// original, ping6 is preferred for IPv6 when available locally.
func pingCommand(ipv int) []string {
	if ipv == 6 {
		if _, err := exec.LookPath("ping6"); err == nil {
			return []string{"ping6", "-c", "1"}
		}
	}
	return []string{"ping", "-c", "1"}
}
