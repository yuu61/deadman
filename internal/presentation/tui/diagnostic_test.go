package tui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/mattn/go-runewidth"

	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestICMPRemediesMatchPlatformAndCapability(t *testing.T) {
	for _, platform := range []probe.OS{
		probe.OSLinux, probe.OSDarwin, probe.OSWindows, probe.OSFreeBSD, "",
	} {
		for _, d := range []monitoring.Diagnostic{
			monitoring.DirectICMPUnavailable{Platform: platform},
			monitoring.NexthopICMPUnavailable{Platform: platform},
		} {
			lines := diagnosticLines([]monitoring.Diagnostic{d})

			text := strings.Join(lines, "\n")
			if !strings.Contains(text, "native ICMP unavailable") {
				t.Fatalf("missing diagnosis: %q", text)
			}

			if platform != probe.OSLinux {
				if strings.Contains(text, "setcap") || strings.Contains(text, "sysctl") ||
					strings.Contains(text, "CAP_NET_RAW") {
					t.Fatalf("Linux remedy on %s: %q", platform, text)
				}

				continue
			}

			if len(lines) != 2 {
				t.Fatalf("remedy needs a separate line: %q", lines)
			}

			prefix, _, found := strings.Cut(lines[1], "setcap cap_net_raw+ep")
			if !found || runewidth.StringWidth(prefix)+2 >= 80 {
				t.Fatalf("remedy clipped on an 80-column terminal: %q", lines[1])
			}

			_, direct := d.(monitoring.DirectICMPUnavailable)
			if strings.Contains(text, "sysctl") != direct {
				t.Fatalf("datagram remedy does not match capability: %q", text)
			}
		}
	}
}

func TestDiagnosticDetailsAndOrder(t *testing.T) {
	lines := diagnosticLines([]monitoring.Diagnostic{
		monitoring.TargetBuildFailed{Name: "bad", Addr: "192.0.2.1", Reason: "missing community"},
		monitoring.ReloadFailed{Reason: "missing.conf"},
		monitoring.StrictRPFilter{},
		monitoring.SNMPDeprecated{Targets: []string{"agent-a", "agent-b"}},
		monitoring.TargetBuildFailed{Name: "worse", Addr: "192.0.2.2", Reason: "no relay"},
	})
	if len(lines) != 4 ||
		!strings.HasPrefix(lines[0], "2 targets could not be built") ||
		lines[1] != "reload failed: missing.conf" ||
		!strings.HasPrefix(lines[2], "rp_filter is strict") ||
		!strings.HasPrefix(lines[3], "probe=snmp is deprecated") ||
		!strings.HasSuffix(lines[3], ": agent-a, agent-b") {
		t.Fatalf("diagnostic details or order lost: %q", lines)
	}
}

// A header line is laid out one cell per rune, so no East Asian Ambiguous character (an
// em dash, an arrow) may appear in one: a CJK terminal draws it two cells wide, and the
// line would wrap and push the title off the screen.
func TestHeaderTextHasNoAmbiguousWidth(t *testing.T) {
	lines := diagnosticLines([]monitoring.Diagnostic{
		monitoring.UnterminatedQuote{Name: "a"},
		monitoring.DroppedTokens{Name: "a", Addr: "192.0.2.1", Tokens: []string{"x"}},
		monitoring.TargetBuildFailed{Name: "a", Addr: "192.0.2.1", Reason: "r"},
		monitoring.ReloadFailed{Reason: "r"},
		monitoring.DirectICMPUnavailable{Platform: probe.OSLinux},
		monitoring.NexthopICMPUnavailable{Platform: probe.OSLinux},
		monitoring.StrictRPFilter{},
		monitoring.SNMPDeprecated{Targets: []string{"a"}},
	})
	lines = append(lines, Model{}.keysLine(), scaleFlagWarning(-1), colsFlagWarning(0),
		logDroppedLine(3), overflowLine(2))

	for _, line := range lines {
		for _, r := range line {
			if r > unicode.MaxASCII {
				t.Errorf("header text %q holds %q, which may be two cells wide", line, r)
			}
		}
	}
}
