package ping

import "testing"

// selectMethod and Describe must agree with New's dispatch precedence: tcp >
// via=snmp/netns/vrf/routers_api > relay (ssh) > nexthop > direct.
func TestSelectMethodAndDescribe(t *testing.T) {
	cases := []struct {
		name     string
		spec     Spec
		method   Method
		describe string
	}{
		{
			name:     "direct",
			spec:     Spec{Addr: "1.1.1.1"},
			method:   MethodDirect,
			describe: "direct",
		},
		{
			name:     "nexthop",
			spec:     Spec{Addr: "1.1.1.1", Relay: map[string]string{"nexthop": "10.0.0.1"}},
			method:   MethodNexthop,
			describe: "nexthop 10.0.0.1",
		},
		{
			// A literal IPv6 target cannot be force-routed: the method is still
			// MethodNexthop (it dispatches the nexthop pinger), but the probe falls
			// back to ordinary routing, so the label must read "direct", not nexthop.
			name: "nexthop_ipv6_falls_back",
			spec: Spec{
				Addr:  "2001:db8::1",
				Relay: map[string]string{"nexthop": "fe80::1"},
			},
			method:   MethodNexthop,
			describe: "direct",
		},
		{
			name:     "ssh",
			spec:     Spec{Addr: "1.1.1.1", Relay: map[string]string{"relay": "h"}},
			method:   MethodSSH,
			describe: "ssh h",
		},
		{
			name:     "snmp",
			spec:     Spec{Addr: "1.1.1.1", Relay: map[string]string{"via": "snmp", "relay": "h"}},
			method:   MethodSNMP,
			describe: "snmp h",
		},
		{
			name: "netns",
			spec: Spec{
				Addr:  "1.1.1.1",
				Relay: map[string]string{"via": "netns", "relay": "ns"},
			},
			method:   MethodNetns,
			describe: "netns ns",
		},
		{
			name:     "vrf",
			spec:     Spec{Addr: "1.1.1.1", Relay: map[string]string{"via": "vrf", "relay": "v"}},
			method:   MethodVRF,
			describe: "vrf v",
		},
		{
			name: "routeros",
			spec: Spec{
				Addr:  "1.1.1.1",
				Relay: map[string]string{"via": "routers_api", "relay": "r"},
			},
			method:   MethodRouterOS,
			describe: "routeros r",
		},
		{
			// The README spelling must resolve to RouterOS too, not fall through
			// to the SSH relay.
			name: "routeros_readme_spelling",
			spec: Spec{
				Addr:  "1.1.1.1",
				Relay: map[string]string{"via": "routeros_api", "relay": "r"},
			},
			method:   MethodRouterOS,
			describe: "routeros r",
		},
		{
			name:     "tcp",
			spec:     Spec{Addr: "1.1.1.1", TCP: "dstport:80"},
			method:   MethodTCP,
			describe: "tcp dstport:80",
		},
		{
			// relay takes precedence over nexthop (the relay mode wins).
			name: "relay_beats_nexthop",
			spec: Spec{
				Addr:  "1.1.1.1",
				Relay: map[string]string{"relay": "h", "nexthop": "10.0.0.1"},
			},
			method:   MethodSSH,
			describe: "ssh h",
		},
		{
			// tcp takes precedence over everything.
			name: "tcp_beats_relay",
			spec: Spec{
				Addr:  "1.1.1.1",
				TCP:   "dstport:80",
				Relay: map[string]string{"relay": "h"},
			},
			method:   MethodTCP,
			describe: "tcp dstport:80",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := selectMethod(c.spec); got != c.method {
				t.Errorf("selectMethod = %d, want %d", got, c.method)
			}

			if got := Describe(c.spec); got != c.describe {
				t.Errorf("Describe = %q, want %q", got, c.describe)
			}
		})
	}
}
