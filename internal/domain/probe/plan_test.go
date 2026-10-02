package probe

import (
	"net/netip"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, s Spec) Plan {
	t.Helper()

	p, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestCompileRejectsInvalidConditions(t *testing.T) {
	for _, s := range []Spec{
		{},
		{Addr: "host", Params: TCP{}},
		{Addr: "host", Params: TCP{Port: PortNumber(0)}},
		{Addr: "host", Params: TCP{Port: PortNumber(65536)}},
		{Addr: "host", Params: TCP{Port: PortNumber(80), Family: Family(99)}},
		{Addr: "192.0.2.1", Params: TCP{Port: PortNumber(80), Family: FamilyIPv6}},
		{Addr: "2001:db8::1", Params: TCP{Port: PortNumber(80), Family: FamilyIPv4}},
		{Addr: "host", Params: QUIC{Port: PortNumber(-1)}},
		{Addr: "host", Params: QUIC{Port: PortNumber(0)}},
		{Addr: "host", Params: QUIC{Verify: Verification(99)}},
		{Addr: "host", Params: QUIC{ALPN: strings.Repeat("x", 256)}},
		{Addr: "host", Params: Direct{Family: Family(99)}},
		{Addr: "192.0.2.1", Params: Direct{Family: FamilyIPv6}},
		{Addr: "192.0.2.1", Params: QUIC{Family: FamilyIPv6}},
		{Addr: "192.0.2.1", Params: SSH{Host: "jump"}},
		{Addr: "remote.internal", Params: SSH{Host: "jump", OS: OSLinux}},
		{Addr: "192.0.2.1", Params: SSH{Host: "jump", OS: OSWindows}},
		{Addr: "192.0.2.1", Params: Netns{}},
		{Addr: "192.0.2.1", Params: VRF{}},
		{Addr: "192.0.2.1", Params: SNMP{Host: "agent"}},
		{Addr: "192.0.2.1", Params: RouterOS{Host: "r", Username: "u"}},
		{
			Addr: "192.0.2.1",
			Params: RouterOS{
				Host:     "r",
				Username: "u",
				Password: "p",
				Scheme:   "ftp",
			},
		},

		{Addr: "192.0.2.1", Params: Nexthop{}},
		{Addr: "192.0.2.1", Params: Nexthop{Gateway: netip.MustParseAddr("2001:db8::1")}},
		{Addr: "fe80::2", Params: Nexthop{Gateway: netip.MustParseAddr("fe80::1%eth0")}},
		// The probe leaves by the gateway's link, so a target's zone would do nothing and
		// only split one path into two identities.
		{
			Addr:   "fe80::2%eth9",
			Params: Nexthop{Gateway: netip.MustParseAddr("fe80::1"), Source: SourceInterface("eth1")},
		},
	} {
		p, err := Compile(s)
		if err == nil {
			t.Errorf("accepted %+v: %+v", s, p)
		}
	}
}

// A source is checked by Compile, not first by the adapter that sends from it: every
// case here is decidable without I/O, and slipping through would leave the row failing
// every round (or sending from somewhere else) instead of being rejected with a reason.
func TestCompileRejectsUnusableSources(t *testing.T) {
	v4, v6 := SourceAddr(
		netip.MustParseAddr("192.0.2.9"),
	), SourceAddr(
		netip.MustParseAddr("2001:db8::9"),
	)
	gw4, gw6 := netip.MustParseAddr("192.0.2.254"), netip.MustParseAddr("2001:db8::fe")
	linkLocalGW := netip.MustParseAddr("fe80::1")

	for name, s := range map[string]Spec{
		"direct v6 source, v4 literal": {Addr: "192.0.2.1", Params: Direct{Source: v6}},
		"direct v4 source, v6 literal": {Addr: "::1", Params: Direct{Source: v4}},
		"direct v4 source, explicit v6": {
			Addr:   "example.com",
			Params: Direct{Source: v4, Family: FamilyIPv6},
		},
		"interface named like an address": {
			Addr:   "192.0.2.1",
			Params: Direct{Source: SourceInterface("192.0.2.9")},
		},
		"link-local gateway without source": {
			Addr:   "fe80::2",
			Params: Nexthop{Gateway: linkLocalGW},
		},
		"link-local gateway with an address source": {
			Addr:   "2001:db8::1",
			Params: Nexthop{Gateway: linkLocalGW, Source: v6},
		},
		"nexthop source of the other family": {
			Addr:   "192.0.2.1",
			Params: Nexthop{Gateway: gw4, Source: v6},
		},
		"nexthop zoned source": {
			Addr:   "2001:db8::1",
			Params: Nexthop{Gateway: gw6, Source: SourceAddr(netip.MustParseAddr("fe80::9%eth0"))},
		},
		"interface source on a Darwin relay": {
			Addr:   "192.0.2.1",
			Params: SSH{Host: "jump", OS: OSDarwin, Source: SourceInterface("en0")},
		},
		"source on a FreeBSD relay": {
			Addr:   "192.0.2.1",
			Params: SSH{Host: "jump", OS: OSFreeBSD, Source: v4},
		},
		"relay host name with only an interface source": {
			Addr:   "remote.internal",
			Params: SSH{Host: "jump", OS: OSLinux, Source: SourceInterface("eth1")},
		},
		"relay source of the other family": {
			Addr:   "192.0.2.1",
			Params: Netns{Name: "ns", Source: v6},
		},
		// An IPv4 address has no zone, so the mapped source's would be lost, as for a
		// destination; for a nexthop that would also dodge its zoned-source rule.
		"zoned IPv4-mapped direct source": {
			Addr:   "192.0.2.1",
			Params: Direct{Source: SourceAddr(netip.MustParseAddr("::ffff:192.0.2.9%eth0"))},
		},
		"zoned IPv4-mapped nexthop source": {
			Addr:   "192.0.2.1",
			Params: Nexthop{Gateway: gw4, Source: SourceAddr(netip.MustParseAddr("::ffff:192.0.2.9%eth0"))},
		},
	} {
		p, err := Compile(s)
		if err == nil {
			t.Errorf("%s: accepted %+v", name, p.Params())
		}
	}
}

func TestCompiledDefaultsHaveIdenticalPaths(t *testing.T) {
	for _, c := range []struct {
		method            Method
		addr              string
		omitted, explicit Params
	}{
		{MethodDirect, "192.0.2.1", Direct{}, Direct{Family: FamilyIPv4}},
		{MethodTCP, "example.com", TCP{Port: PortNumber(80)}, TCP{Port: PortNumber(80), Family: FamilyIPv4}},
		{MethodTCP, "2001:db8::1", TCP{Port: PortNumber(80)}, TCP{Port: PortNumber(80), Family: FamilyIPv6}},
		{
			MethodQUIC,
			"example.com",
			QUIC{},
			QUIC{Port: PortNumber(443), ALPN: "h3", SNI: "example.com", Verify: VerifyDisabled},
		},
		// A source address fixes the family a name resolves in.
		{
			MethodDirect,
			"example.com",
			Direct{Source: SourceAddr(netip.MustParseAddr("::ffff:192.0.2.9"))},
			Direct{Source: SourceAddr(netip.MustParseAddr("192.0.2.9")), Family: FamilyIPv4},
		},
		{
			MethodRouterOS,
			"192.0.2.1",
			RouterOS{
				Host:     "r",
				Username: "u",
				Password: "old",
			},
			RouterOS{
				Host:     "r",
				Scheme:   "https",
				Username: "new",
				Password: "new",
				Verify:   VerifyEnabled,
			},
		},

		{MethodNetns, "192.0.2.1", Netns{Name: "ns"}, Netns{Name: "ns", Family: FamilyIPv4}},
		{MethodVRF, "192.0.2.1", VRF{Name: "v"}, VRF{Name: "v", Family: FamilyIPv4}},
		{
			MethodSSH,
			"192.0.2.1",
			SSH{
				Host: "jump",
				OS:   OSLinux,
			},
			SSH{
				Host:   "jump",
				OS:     OSLinux,
				Family: FamilyIPv4,
				User:   "user",
				Key:    "secret",
			},
		},

		{
			MethodNexthop,
			"192.0.2.1",
			Nexthop{Gateway: netip.MustParseAddr("::ffff:192.0.2.254")},
			Nexthop{Gateway: netip.MustParseAddr("192.0.2.254")},
		},
	} {
		a := mustCompile(t, Spec{Addr: c.addr, Params: c.omitted})
		if a.Method() != c.method {
			t.Errorf("%s: compiled as %s", c.method, a.Method())
		}

		b := mustCompile(t, Spec{Addr: c.addr, Params: c.explicit})
		if a.Identity() != b.Identity() {
			t.Errorf("%s: equivalent paths differ: %s / %s", c.method, a.Identity(), b.Identity())
		}

		if strings.Contains(b.Identity(), "secret") {
			t.Fatal("identity contains credentials")
		}
	}
}

func TestIdentitySeparatesConditions(t *testing.T) {
	cases := []Spec{
		{Addr: "example.com", Params: Direct{}},
		{Addr: "example.com", Params: Direct{Family: FamilyIPv4}},
		{Addr: "example.com", Params: Direct{Family: FamilyIPv6}},
		{Addr: "example.com", Params: Direct{Source: SourceInterface("eth0")}},
		{Addr: "example.com", Params: TCP{Port: PortNumber(80)}},
		{Addr: "example.com", Params: TCP{Port: PortNumber(443)}},
		{Addr: "example.com", Params: TCP{Port: PortNumber(80), Family: FamilyIPv6}},
		{Addr: "example.com", Params: QUIC{}},
		{Addr: "example.com", Params: QUIC{ALPN: "hq"}},
		{Addr: "example.com", Params: QUIC{SNI: "other"}},
		{Addr: "a:b", Params: Direct{Source: SourceInterface("c")}},
		{Addr: "a", Params: Direct{Source: SourceInterface("b:c")}},
		{
			Addr:   "example.com",
			Params: Netns{Name: "ns", Family: FamilyIPv4},
		},
		{Addr: "example.com", Params: VRF{Name: "ns", Family: FamilyIPv4}},
	}
	seen := map[string]bool{}

	for _, s := range cases {
		id := mustCompile(t, s).Identity()
		if seen[id] {
			t.Errorf("duplicate identity: %s", id)
		}

		seen[id] = true
	}
}

func TestIdentitySeparatesVerification(t *testing.T) {
	for _, c := range []struct {
		name     string
		disabled Params
		enabled  Params
	}{
		{
			name:     "quic",
			disabled: QUIC{Verify: VerifyDisabled},
			enabled:  QUIC{Verify: VerifyEnabled},
		},
		{
			name: "routeros",
			disabled: RouterOS{
				Host: "router", Username: "admin", Password: "secret", Verify: VerifyDisabled,
			},
			enabled: RouterOS{
				Host: "router", Username: "admin", Password: "secret", Verify: VerifyEnabled,
			},
		},
	} {
		off := mustCompile(t, Spec{Addr: "example.com", Params: c.disabled})

		on := mustCompile(t, Spec{Addr: "example.com", Params: c.enabled})
		if off.Identity() == on.Identity() {
			t.Errorf("%s: changing TLS verification kept the same identity", c.name)
		}
	}
}

func TestCompileResolvesParams(t *testing.T) {
	for _, c := range []struct {
		s    Spec
		want Params
	}{
		{
			Spec{
				Addr:   "host",
				Params: QUIC{},
			},
			QUIC{
				Port:   PortNumber(443),
				ALPN:   "h3",
				SNI:    "host",
				Verify: VerifyDisabled,
			},
		},

		{
			Spec{
				Addr:   "fe80::1%eth0",
				Params: QUIC{},
			},
			QUIC{
				Port:   PortNumber(443),
				ALPN:   "h3",
				Family: FamilyIPv6,
				Verify: VerifyDisabled,
			},
		},

		{
			Spec{
				Addr:   "192.0.2.1",
				Params: Netns{Name: "ns"},
			},
			Netns{
				Name:   "ns",
				Family: FamilyIPv4,
			},
		},

		{
			Spec{
				Addr: "remote.internal",
				Params: SSH{
					Host:   "jump",
					OS:     OSDarwin,
					Family: FamilyIPv6,
				},
			},
			SSH{
				Host:   "jump",
				OS:     OSDarwin,
				Family: FamilyIPv6,
			},
		},

		// A zoned source address keeps its zone; a source address fixes a name's family.
		{
			Spec{
				Addr:   "router.local",
				Params: Direct{Source: SourceAddr(netip.MustParseAddr("fe80::9%eth0"))},
			},
			Direct{Source: SourceAddr(netip.MustParseAddr("fe80::9%eth0")), Family: FamilyIPv6},
		},

		{
			Spec{
				Addr:   "remote.internal",
				Params: SSH{Host: "jump", OS: OSLinux, Source: SourceAddr(netip.MustParseAddr("2001:db8::9"))},
			},
			SSH{
				Host:   "jump",
				OS:     OSLinux,
				Source: SourceAddr(netip.MustParseAddr("2001:db8::9")),
				Family: FamilyIPv6,
			},
		},
	} {
		if got := mustCompile(t, c.s).Params(); got != c.want {
			t.Errorf("got %+v, want %+v", got, c.want)
		}
	}
}

func TestEveryMethodHasName(t *testing.T) {
	for _, m := range Methods() {
		if m.String() == "" || strings.HasPrefix(m.String(), "method(") {
			t.Errorf("method %d has no name", m)
		}
	}
}

// An omitted port and a written 0 are different: omission takes the method's default or
// names the missing attribute, while 0 is an out-of-range port like any other.
func TestPortOmissionIsNotZero(t *testing.T) {
	_, err := Compile(Spec{Addr: "host", Params: TCP{}})
	if err == nil || !strings.Contains(err.Error(), "tcp requires port") {
		t.Errorf("tcp without a port: %v", err)
	}

	_, err = Compile(Spec{Addr: "host", Params: TCP{Port: PortNumber(0)}})
	if err == nil || !strings.Contains(err.Error(), "invalid port 0") {
		t.Errorf("tcp port 0: %v", err)
	}

	q, ok := mustCompile(t, Spec{Addr: "host", Params: QUIC{}}).Params().(QUIC)
	if !ok || q.Port != PortNumber(defaultQUICPort) {
		t.Errorf("quic without a port = %+v, want the default", q.Port)
	}

	_, err = Compile(Spec{Addr: "host", Params: QUIC{Port: PortNumber(0)}})
	if err == nil {
		t.Error("quic port 0 took the default")
	}
}

// An SNMP probe hands the agent the address bytes without this host's zone.
// Unusable target and relay spellings must be rejected before execution.
func TestCompileRejectsTargetsTheToolCannotCarry(t *testing.T) {
	tcp := TCP{Port: PortNumber(80)}
	agent := SNMP{Host: "agent", Community: "public"}

	for name, s := range map[string]Spec{
		"snmp zoned literal":          {Addr: "fe80::1%eth0", Params: agent},
		"snmp transport spec":         {Addr: "192.0.2.1", Params: SNMP{Host: "udp6:[::1]:161", Community: "c"}},
		"snmp agent port":             {Addr: "192.0.2.1", Params: SNMP{Host: "agent:1161", Community: "c"}},
		"snmp bracketed agent port":   {Addr: "192.0.2.1", Params: SNMP{Host: "[::1]:161", Community: "c"}},
		"snmp agent over tcp":         {Addr: "192.0.2.1", Params: SNMP{Host: "tcp:agent", Community: "c"}},
		"snmp agent not a host":       {Addr: "192.0.2.1", Params: SNMP{Host: "agent|1", Community: "c"}},
		"zoned IPv4-mapped literal":   {Addr: "::ffff:192.0.2.1%eth0"},
		"zoned padded mapped literal": {Addr: "::ffff:192.000.002.001%eth0"},
		"name with a space":           {Addr: "web 1"},
		"name with a wide space":      {Addr: "web\u30001"},
		"name with a control":         {Addr: "web\x1b[1m"},
		"quic name with a tab":        {Addr: "web\t1", Params: QUIC{}},
		"ssh relay with a space": {Addr: "192.0.2.1", Params: SSH{
			Host: "jump host", OS: OSLinux,
		}},
	} {
		p, err := Compile(s)
		if err == nil {
			t.Errorf("%s: accepted %s", name, p.Destination())
		}
	}

	for name, s := range map[string]Spec{
		"tcp IPv6 literal":        {Addr: "2001:db8::1", Params: tcp},
		"tcp zoned literal":       {Addr: "fe80::1%eth0", Params: tcp},
		"tcp IPv4-mapped literal": {Addr: "::ffff:192.0.2.1", Params: tcp},
		"tcp host name":           {Addr: "example.com", Params: tcp},
		"snmp IPv6 literal":       {Addr: "2001:db8::1", Params: agent},
		"snmp IPv6 agent":         {Addr: "192.0.2.1", Params: SNMP{Host: "2001:db8::161", Community: "c"}},
		"snmp zoned agent":        {Addr: "192.0.2.1", Params: SNMP{Host: "fe80::161%eth0", Community: "c"}},
		"internationalized name":  {Addr: "bücher.example"},
		"ssh relay with a user":   {Addr: "192.0.2.1", Params: SSH{Host: "ops@jump", OS: OSLinux}},
	} {
		_, err := Compile(s)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An IP literal target reaches every adapter in its one canonical spelling, an
// IPv4-mapped one as IPv4 — a relay's `ping -4` cannot parse ::ffff:192.0.2.1
// — so two spellings of an address are one path. A host name is kept as written.
func TestCompileCanonicalizesLiteralTargets(t *testing.T) {
	jump := SSH{Host: "jump", OS: OSLinux}

	for _, c := range []struct {
		written, want string
		params        Params
	}{
		{"::ffff:192.0.2.1", "192.0.2.1", jump},
		{"::ffff:192.0.2.1", "192.0.2.1", TCP{Port: PortNumber(80)}},
		{"192.000.002.001", "192.0.2.1", jump},
		{"008.008.008.008", "8.8.8.8", TCP{Port: PortNumber(80)}},
		{"::ffff:192.000.002.001", "192.0.2.1", nil},
		{"2001:DB8:0::1", "2001:db8::1", nil},
		{"FE80::1%eth0", "fe80::1%eth0", nil},
		{"Example.COM", "Example.COM", nil},
	} {
		written := mustCompile(t, Spec{Addr: c.written, Params: c.params})
		canonical := mustCompile(t, Spec{Addr: c.want, Params: c.params})

		if got := written.Destination().String(); got != c.want {
			t.Errorf("%s: plan address %s, want %s", c.written, got, c.want)
		}

		if written.Identity() != canonical.Identity() {
			t.Errorf("%s and %s are different paths", c.written, c.want)
		}
	}
}
