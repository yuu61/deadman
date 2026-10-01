package prober

import (
	"runtime"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Host reports this host's probing capabilities: which socket paths the probing modes
// here can open, and the platform facts that decide whether a forced next-hop's replies
// come back.
// The startup warnings ask it (it satisfies monitoring.HostCapabilities), so they judge
// by the very checks the modes themselves use.
type Host struct{}

// DirectICMPAvailable reports whether native direct ICMP can open a socket here: the
// raw socket or the unprivileged datagram one.
func (Host) DirectICMPAvailable() bool { return directICMPAvailable() }

// RawICMPAvailable reports whether the privileged raw-socket path can be opened here
// (root or CAP_NET_RAW). A forced next-hop needs this specifically — it sends via
// AF_PACKET, for which the unprivileged datagram path cannot substitute.
func (Host) RawICMPAvailable() bool { return useICMPPrivileged() }

// RPFilterStrict reports whether Linux reverse-path filtering is strict on some
// interface, which can drop the replies to cross-interface forced IPv4 probes.
func (Host) RPFilterStrict() bool { return rpFilterStrict() }

// Platform identifies the OS whose capabilities this adapter diagnoses.
func (Host) Platform() probe.OS { return hostOS() }

// hostOS maps this host's GOOS to its canonical `uname -s` name, the platform whose
// privilege remedies the startup warnings offer (Host.Platform). It never picks a relay's
// ping command or source flag: those follow the relay's OS in the plan.
func hostOS() probe.OS {
	switch runtime.GOOS {
	case "linux":
		return probe.OSLinux
	case "darwin":
		return probe.OSDarwin
	case "freebsd":
		return probe.OSFreeBSD
	case "windows":
		return probe.OSWindows
	default:
		return probe.OS(runtime.GOOS)
	}
}
