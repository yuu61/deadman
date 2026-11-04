package tui

import (
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/monitor"
)

// identity is what a row's HOSTNAME and ADDRESS columns show.
type identity struct{ name, addr string }

// rowIdentity returns the name and address a line shows; static lines show none.
func rowIdentity(line monitoring.Line) identity {
	switch l := line.(type) {
	case monitoring.Monitored:
		return identity{l.Target.Name, addressLabel(l.Target.Addr, l.Plan)}
	case monitoring.Rejected:
		return identity{l.Name, l.Addr}
	case monitoring.Blank, monitoring.Separator, monitoring.ColumnBreak:
		return identity{}
	default:
		return identity{} // unreachable: monitoring.Line is a closed set, each kind cased above.
	}
}

// rowStats returns the statistics a line shows; ok is false for static lines. A row that
// could not be built was never probed, so it shows the statistics of no probe at all.
func rowStats(line monitoring.Line) (monitor.Stats, bool) {
	switch l := line.(type) {
	case monitoring.Monitored:
		return l.Target.Stats, true
	case monitoring.Rejected:
		return monitor.Stats{}, true
	case monitoring.Blank, monitoring.Separator, monitoring.ColumnBreak:
		return monitor.Stats{}, false
	default:
		return monitor.Stats{}, false // unreachable: monitoring.Line is a closed set, each kind cased above.
	}
}
