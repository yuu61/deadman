package tui

import (
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// rejectedLabel is the VIA label of a row that could not be built.
const rejectedLabel = "error"

const quicPortWithoutDetail = 443

// rowLabel is a line's VIA label: how its plan probes it, or "error" for a row that could
// not be built. A separator has none.
func rowLabel(line monitoring.Line) string {
	switch l := line.(type) {
	case monitoring.Monitored:
		return planLabel(l.Plan)
	case monitoring.Rejected:
		return rejectedLabel
	case monitoring.Separator:
		return ""
	default:
		return "" // unreachable: monitoring.Line is a closed set, each kind cased above.
	}
}

// planLabel names the selected method and the detail that tells its targets apart.
func planLabel(plan probe.Plan) string {
	method := plan.Method().String()
	if plan.Method() == probe.MethodQUIC {
		method = "QUIC"
	}

	detail := detailFor(plan.Params())
	if detail == "" {
		return method
	}

	return method + " " + detail
}

func detailFor(params probe.Params) string {
	switch p := params.(type) {
	case probe.LocalParams:
		return localDetail(p)
	case probe.RelayParams:
		return relayDetail(p)
	default:
		return ""
	}
}

// localDetail tells apart the targets of a method this host probes by itself: the port
// of a TCP probe or a QUIC probe using a port other than 443, the gateway of a forced one.
func localDetail(params probe.LocalParams) string {
	switch p := params.(type) {
	case probe.Direct:
		return ""
	case probe.TCP:
		return p.Port.String()
	case probe.QUIC:
		if p.Port == probe.PortNumber(quicPortWithoutDetail) {
			return ""
		}

		return p.Port.String()
	case probe.Nexthop:
		return p.Gateway.String()
	default:
		return ""
	}
}

// relayDetail tells apart the targets of a relayed method by the relay.
func relayDetail(params probe.RelayParams) string {
	switch p := params.(type) {
	case probe.SNMP:
		return p.Host
	case probe.SSH:
		return p.Host
	case probe.Netns:
		return p.Name
	case probe.VRF:
		return p.Name
	case probe.RouterOS:
		return p.Relay()
	default:
		return ""
	}
}
