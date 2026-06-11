package ping

import (
	"slices"
	"testing"
)

// The ping binary is chosen by the target's OS, not by probing a local ping6 (which
// is irrelevant to an ssh remote): macOS and FreeBSD use ping6, while Linux uses
// the unified `ping -6`. IPv4 is always plain ping.
func TestPingCommand(t *testing.T) {
	cases := []struct {
		name   string
		ipv    int
		osname string
		want   []string
	}{
		{"ipv4_linux", ipv4, osLinux, []string{cmdPing, "-c", "1"}},
		{"ipv4_darwin", ipv4, osDarwin, []string{cmdPing, "-c", "1"}},
		{"ipv6_linux", ipv6, osLinux, []string{cmdPing, "-6", "-c", "1"}},
		{"ipv6_freebsd", ipv6, osFreeBSD, []string{cmdPing6, "-c", "1"}},
		{"ipv6_darwin", ipv6, osDarwin, []string{cmdPing6, "-c", "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pingCommand(c.ipv, c.osname); !slices.Equal(got, c.want) {
				t.Errorf("pingCommand(%d, %q) = %v, want %v", c.ipv, c.osname, got, c.want)
			}
		})
	}
}

// source= is meaningless for the snmp / tcp(hping3) / routeros / quic modes (the
// probe originates at the relay, hping3 has no per-probe source, or the QUIC dial
// binds [::]:0), so SourceUnsupported flags them for a TUI warning. The
// source-capable modes return false.
func TestSourceUnsupported(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want bool
	}{
		{
			"snmp",
			Spec{
				Addr:   "1.2.3.4",
				Source: "10.0.0.1",
				Relay:  map[string]string{"via": "snmp", "relay": "h", "community": "c"},
			},
			true,
		},
		{"tcp", Spec{Addr: "1.2.3.4", Source: "10.0.0.1", TCP: "dstport:80"}, true},
		{
			"routeros",
			Spec{
				Addr:   "1.2.3.4",
				Source: "10.0.0.1",
				Relay: map[string]string{
					"via":      "routeros_api",
					"relay":    "h",
					"username": "u",
					"password": "p",
				},
			},
			true,
		},
		{
			"quic",
			Spec{
				Addr:   "1.2.3.4",
				Source: "10.0.0.1",
				Relay:  map[string]string{"via": "quic"},
			},
			true,
		},
		{"direct", Spec{Addr: "1.2.3.4", Source: "10.0.0.1"}, false},
		{
			"nexthop",
			Spec{Addr: "1.2.3.4", Source: "eth0", Relay: map[string]string{"nexthop": "10.0.0.1"}},
			false,
		},
		{
			"ssh_relay",
			Spec{Addr: "1.2.3.4", Source: "10.0.0.1", Relay: map[string]string{"relay": "h"}},
			false,
		},
		{
			"no_source",
			Spec{
				Addr:  "1.2.3.4",
				Relay: map[string]string{"via": "snmp", "relay": "h", "community": "c"},
			},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SourceUnsupported(c.spec); got != c.want {
				t.Errorf("SourceUnsupported = %v, want %v", got, c.want)
			}
		})
	}
}

// A source on snmp/tcp/routeros is ignored (the TUI warns), not rejected, so New
// still builds a working pinger rather than aborting startup.
func TestSourceIgnoredNotRejected(t *testing.T) {
	specs := []Spec{
		{
			Addr:   "1.2.3.4",
			Source: "10.0.0.1",
			Relay:  map[string]string{"via": "snmp", "relay": "h", "community": "c"},
		},
		{Addr: "1.2.3.4", Source: "10.0.0.1", TCP: "dstport:80"},
		{
			Addr:   "1.2.3.4",
			Source: "10.0.0.1",
			Relay: map[string]string{
				"via":      "routeros_api",
				"relay":    "h",
				"username": "u",
				"password": "p",
			},
		},
		{Addr: "1.2.3.4", Source: "10.0.0.1", Relay: map[string]string{"via": "quic"}},
	}
	for _, s := range specs {
		_, err := New(s)
		if err != nil {
			t.Errorf("New(%v) errored on an ignorable source: %v", s.Relay, err)
		}
	}
}

// selectMethod and Describe must agree with New's dispatch precedence: tcp >
// via=snmp/netns/vrf/routers_api/quic > relay (ssh) > nexthop > direct.
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
			// An IPv6 target is force-routed like IPv4 (via NDP), here through a
			// link-local gateway, so the label names the gateway.
			name: "nexthop_ipv6",
			spec: Spec{
				Addr:  "2001:db8::1",
				Relay: map[string]string{"nexthop": "fe80::1"},
			},
			method:   MethodNexthop,
			describe: "nexthop fe80::1",
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
			name:     "quic",
			spec:     Spec{Addr: "1.1.1.1", Relay: map[string]string{"via": "quic"}},
			method:   MethodQUIC,
			describe: "quic 443",
		},
		{
			// A non-default port= is reflected in the VIA label.
			name: "quic_port",
			spec: Spec{
				Addr:  "1.1.1.1",
				Relay: map[string]string{"via": "quic", "port": "8443"},
			},
			method:   MethodQUIC,
			describe: "quic 8443",
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
