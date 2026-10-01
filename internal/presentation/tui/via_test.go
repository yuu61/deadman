package tui

import (
	"net/netip"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// planLabel names the explicitly selected method and the detail
// that tells its targets apart.
func TestPlanLabel(t *testing.T) {
	cases := []struct {
		name     string
		spec     probe.Spec
		method   probe.Method
		describe string
	}{
		{
			name:     "direct",
			spec:     probe.Spec{Addr: "1.1.1.1"},
			method:   probe.MethodDirect,
			describe: "direct",
		},
		{
			name: "nexthop",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.Nexthop{Gateway: netip.MustParseAddr("10.0.0.1")},
			},
			method:   probe.MethodNexthop,
			describe: "nexthop 10.0.0.1",
		},
		{
			// An IPv6 target is force-routed like IPv4 (via NDP), here through a
			// link-local gateway, so the label names the gateway.
			name: "nexthop_ipv6",
			spec: probe.Spec{
				Addr: "2001:db8::1",
				Params: probe.Nexthop{
					Gateway: netip.MustParseAddr("fe80::1"),
					Source:  probe.SourceInterface("eth0"),
				},
			},
			method:   probe.MethodNexthop,
			describe: "nexthop fe80::1",
		},
		{
			name: "ssh",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.SSH{Host: "h", OS: probe.OSLinux},
			},
			method:   probe.MethodSSH,
			describe: "ssh h",
		},
		{
			name: "snmp",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.SNMP{Host: "h", Community: "public"},
			},
			method:   probe.MethodSNMP,
			describe: "snmp h",
		},
		{
			name: "netns",
			spec: probe.Spec{
				Addr: "1.1.1.1", Params: probe.Netns{Name: "ns"},
			},
			method:   probe.MethodNetns,
			describe: "netns ns",
		},
		{
			name: "vrf",
			spec: probe.Spec{
				Addr: "1.1.1.1", Params: probe.VRF{Name: "v"},
			},
			method:   probe.MethodVRF,
			describe: "vrf v",
		},
		{
			name: "routeros",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.RouterOS{Host: "r", Username: "u", Password: "p"},
			},
			method:   probe.MethodRouterOS,
			describe: "routeros r",
		},
		{
			name: "routeros_port",
			spec: probe.Spec{
				Addr: "1.1.1.1",
				Params: probe.RouterOS{
					Host:     "r",
					Port:     probe.PortNumber(8443),
					Username: "u",
					Password: "p",
				},
			},
			method:   probe.MethodRouterOS,
			describe: "routeros r:8443",
		},
		{
			name:     "quic",
			spec:     probe.Spec{Addr: "1.1.1.1", Params: probe.QUIC{}},
			method:   probe.MethodQUIC,
			describe: "QUIC",
		},
		{
			name: "quic_explicit_default_port",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.QUIC{Port: probe.PortNumber(443)},
			},
			method:   probe.MethodQUIC,
			describe: "QUIC",
		},
		{
			// A non-default port is reflected in the VIA label.
			name: "quic_port",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.QUIC{Port: probe.PortNumber(8443)},
			},
			method:   probe.MethodQUIC,
			describe: "QUIC 8443",
		},
		{
			name: "tcp",
			spec: probe.Spec{
				Addr:   "1.1.1.1",
				Params: probe.TCP{Port: probe.PortNumber(80)},
			},
			method:   probe.MethodTCP,
			describe: "tcp 80",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := probe.Compile(c.spec)
			if err != nil {
				t.Fatal(err)
			}

			if got := plan.Method(); got != c.method {
				t.Errorf("Method = %v, want %v", got, c.method)
			}

			if got := planLabel(plan); got != c.describe {
				t.Errorf("planLabel = %q, want %q", got, c.describe)
			}
		})
	}
}
