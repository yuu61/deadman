package tui

import (
	"fmt"
	"strings"

	"github.com/yuu61/deadman/internal/config"
)

// startupWarnings collects every operator-facing warning shown under the header:
// config-parse problems first (the most actionable), then next-hop caveats.
func startupWarnings(specs []config.TargetSpec) []string {
	return append(parseWarnings(specs), nexthopWarnings(specs)...)
}

// parseWarnings flags targets whose config line had tokens silently dropped. The
// usual cause is spaces in the name: "Cloudflare via MGMT 1.1.1.1 nexthop=..."
// parses to name=Cloudflare, address=via, with "MGMT" and "1.1.1.1" discarded —
// so the target probes a bogus address and shows 100% loss. Surfacing the dropped
// tokens turns that silent failure into a visible hint.
func parseWarnings(specs []config.TargetSpec) []string {
	var warns []string

	for _, s := range specs {
		if s.UnterminatedQuote {
			warns = append(warns, fmt.Sprintf(
				"%q: unterminated quote in config line — the rest of the line was absorbed "+
					"into one field; close the quote or wrap a name in matching quotes",
				s.Name,
			))
		}

		if len(s.Dropped) == 0 {
			continue
		}

		warns = append(warns, fmt.Sprintf(
			"%q (address=%q): ignored stray tokens [%s] — names with spaces must be quoted; "+
				"only key=value attributes may follow the address",
			s.Name, s.Addr, strings.Join(s.Dropped, " "),
		))
	}

	return warns
}
