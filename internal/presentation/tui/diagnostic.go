package tui

import (
	"fmt"
	"strings"

	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// diagnosticLines words the application's diagnostics as header lines. Remedies get their own line so they
// remain visible when the terminal clips long lines. A config has one build failure per
// bad line, so they share one line, where the first of them was: each rejected row shows
// its own reason in its RESULT column.
func diagnosticLines(diagnostics []monitoring.Diagnostic) []string {
	lines := make([]string, 0, len(diagnostics))
	failed, at := 0, 0

	for _, d := range diagnostics {
		if _, ok := d.(monitoring.TargetBuildFailed); ok {
			if failed == 0 {
				at = len(lines)
				lines = append(lines, "")
			}

			failed++

			continue
		}

		lines = append(lines, formatDiagnostic(d)...)
	}

	if failed > 0 {
		lines[at] = buildFailedLine(failed)
	}

	return lines
}

// buildFailedLine words n targets that could not be built.
func buildFailedLine(n int) string {
	what := "1 target"
	if n != 1 {
		what = fmt.Sprintf("%d targets", n)
	}

	return what + " could not be built and show a permanent failure, each with its reason " +
		"in RESULT; fix the config and reload (R)"
}

// overflowLine words n warnings the header has no room for.
func overflowLine(n int) string {
	return fmt.Sprintf("... and %d more warnings; a taller terminal shows them", n)
}

// logDroppedLine words the result lines the log has dropped.
func logDroppedLine(n int) string {
	return fmt.Sprintf(
		"log: %d result lines dropped; the log directory cannot keep up with the probes", n,
	)
}

// formatDiagnostic words one diagnostic.
func formatDiagnostic(d monitoring.Diagnostic) []string {
	switch d := d.(type) {
	case monitoring.UnterminatedQuote:
		return []string{fmt.Sprintf(
			"%q: unterminated quote in config line; the rest of the line was absorbed "+
				"into one field; close the quote or wrap a name in matching quotes", d.Name,
		)}
	case monitoring.DroppedTokens:
		return []string{fmt.Sprintf(
			"%q (address=%q): ignored stray tokens [%s]; names with spaces must be quoted, and "+
				"only key=value attributes may follow the address",
			d.Name, d.Addr, strings.Join(d.Tokens, " "),
		)}
	case monitoring.ProbeWarning:
		return probeWarningLines(d)
	case monitoring.TargetBuildFailed:
		return []string{buildFailedLine(1)}
	case monitoring.ReloadFailed:
		return []string{"reload failed: " + d.Reason}
	default:
		return []string{"unknown monitoring diagnostic"}
	}
}

// probeWarningLines words the warnings about how the targets will be probed.
func probeWarningLines(w monitoring.ProbeWarning) []string {
	switch d := w.(type) {
	case monitoring.DirectICMPUnavailable:
		return directICMPLines(d.Platform)
	case monitoring.NexthopICMPUnavailable:
		return nexthopICMPLines(d.Platform)
	case monitoring.SNMPDeprecated:
		return []string{
			"probe=snmp is deprecated; few agents implement its RFC 4560 remote ping: " +
				strings.Join(d.Targets, ", "),
		}
	case monitoring.StrictRPFilter:
		return []string{
			"rp_filter is strict: replies to cross-interface next-hop probes may be dropped " +
				"(set net.ipv4.conf.*.rp_filter to 2/loose or 0/off)",
		}
	default:
		return []string{"unknown probing diagnostic"}
	}
}

// nativeICMPUnknown is the ICMP diagnostic off Linux, where no specific remedy is known.
const nativeICMPUnknown = "native ICMP unavailable on this host; check socket permissions and platform support"

func directICMPLines(platform probe.OS) []string {
	if platform != probe.OSLinux {
		return []string{nativeICMPUnknown}
	}

	return []string{
		"native ICMP unavailable: neither raw nor datagram sockets can be opened; direct probes show ?",
		"fix: `sudo setcap cap_net_raw+ep <deadman>`, or check the datagram range with " +
			"`sysctl net.ipv4.ping_group_range`",
	}
}

func nexthopICMPLines(platform probe.OS) []string {
	if platform != probe.OSLinux {
		return []string{nativeICMPUnknown}
	}

	return []string{
		"native ICMP unavailable: raw sockets cannot be opened, so next-hop probes show ?",
		"fix: `sudo setcap cap_net_raw+ep <deadman>`, or run deadman as root " +
			"(a next-hop needs CAP_NET_RAW; ping_group_range does not help it)",
	}
}
