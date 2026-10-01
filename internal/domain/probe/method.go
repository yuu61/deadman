package probe

import "strconv"

// Method identifies one explicitly selected probe method.
type Method int

// Supported probing methods.
const (
	MethodDirect Method = iota
	MethodTCP
	MethodSNMP
	MethodNetns
	MethodVRF
	MethodRouterOS
	MethodQUIC
	MethodSSH
	MethodNexthop
)
const methodCount = int(MethodNexthop) + 1

var methodNames = [...]string{
	"direct",
	"tcp",
	"snmp",
	"netns",
	"vrf",
	"routeros",
	"quic",
	"ssh",
	"nexthop",
}

// Methods returns every supported method.
func Methods() []Method {
	ms := make([]Method, methodCount)
	for i := range ms {
		ms[i] = Method(i)
	}

	return ms
}

func (m Method) String() string {
	if m >= 0 && int(m) < len(methodNames) {
		return methodNames[m]
	}

	return "method(" + strconv.Itoa(int(m)) + ")"
}
