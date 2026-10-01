package monitoring

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// fakeHost reports fixed host capabilities, so a test can stage a precise privilege
// scenario (raw and/or datagram open, rp_filter) without depending on the runner's
// actual CAP_NET_RAW, ping_group_range or platform.
type fakeHost struct {
	direct, raw, rpStrict bool
}

func (fakeHost) Platform() probe.OS { return probe.OSLinux }

func (h fakeHost) DirectICMPAvailable() bool { return h.direct }
func (h fakeHost) RawICMPAvailable() bool    { return h.raw }
func (h fakeHost) RPFilterStrict() bool      { return h.rpStrict }

// capable is a host where every probing path works, so no capability warning fires.
var capable = fakeHost{direct: true, raw: true}

func TestLocalICMPNeeds(t *testing.T) {
	tests := []struct {
		name        string
		spec        config.Target
		wantDirect  bool
		wantNexthop bool
	}{
		{"direct", config.Target{Name: "a", Addr: "8.8.8.8"}, true, false},
		{
			"nexthop",
			config.Target{
				Name:   "a",
				Addr:   "8.8.8.8",
				Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1")},
			},
			false,
			true,
		},
		{
			"ssh relay",
			config.Target{
				Name:   "a",
				Addr:   "8.8.8.8",
				Params: probe.SSH{Host: "host", OS: probe.OSLinux},
			},
			false,
			false,
		},
		{
			"snmp",
			config.Target{
				Name:   "a",
				Addr:   "8.8.8.8",
				Params: probe.SNMP{Host: "agent", Community: "c"},
			},
			false,
			false,
		},
		{
			"tcp",
			config.Target{
				Name:   "a",
				Addr:   "8.8.8.8",
				Params: probe.TCP{Port: probe.PortNumber(80)},
			},
			false,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := localICMPNeeds(planned(t, tt.spec))
			if got.direct != tt.wantDirect || got.nexthop != tt.wantNexthop {
				t.Errorf(
					"localICMPNeeds(%s) = (direct=%v, nexthop=%v), want (%v, %v)",
					tt.name, got.direct, got.nexthop, tt.wantDirect, tt.wantNexthop,
				)
			}
		})
	}
}

func TestLocalICMPNeedsRelayOnly(t *testing.T) {
	targets := planned(
		t,
		config.Target{
			Name:   "s",
			Addr:   "8.8.8.8",
			Params: probe.SSH{Host: "host", OS: probe.OSLinux},
		},
		config.Target{
			Name:   "t",
			Addr:   "8.8.8.8",
			Params: probe.TCP{Port: probe.PortNumber(80)},
		},
	)
	if got := localICMPNeeds(targets); got.direct || got.nexthop {
		t.Errorf(
			"relay-only config: localICMPNeeds = (direct=%v, nexthop=%v), want (false, false)",
			got.direct, got.nexthop,
		)
	}
}

// The application selects facts, without assuming a terminal width or OS remedy.
func TestICMPPrivilegeDiagnosticSelection(t *testing.T) {
	direct := config.Target{Name: "direct", Addr: "192.0.2.1"}
	next := config.Target{
		Name:   "next",
		Addr:   "192.0.2.2",
		Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.254")},
	}
	relay := config.Target{
		Name:   "relay",
		Addr:   "192.0.2.3",
		Params: probe.SSH{Host: "jump", OS: probe.OSLinux},
	}

	cases := []struct {
		name  string
		specs []config.Target
		host  fakeHost
		want  []Diagnostic
	}{
		{
			"direct unavailable",
			[]config.Target{direct},
			fakeHost{},
			[]Diagnostic{DirectICMPUnavailable{Platform: probe.OSLinux}},
		},
		{"relay only", []config.Target{relay}, fakeHost{}, nil},
		{"available", []config.Target{direct, next}, capable, nil},
		{
			"next-hop requires raw",
			[]config.Target{next},
			fakeHost{direct: true},
			[]Diagnostic{NexthopICMPUnavailable{Platform: probe.OSLinux}},
		},
		{
			"mixed with only the datagram socket",
			[]config.Target{direct, next},
			fakeHost{direct: true},
			[]Diagnostic{NexthopICMPUnavailable{Platform: probe.OSLinux}},
		},
		// Neither socket opens, so both classes fail: each is named, or the direct rows
		// would show X with no warning about them.
		{
			"mixed with neither socket",
			[]config.Target{direct, next},
			fakeHost{},
			[]Diagnostic{
				DirectICMPUnavailable{Platform: probe.OSLinux},
				NexthopICMPUnavailable{Platform: probe.OSLinux},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := icmpPrivilegeWarnings(planned(t, c.specs...), c.host)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("diagnostics = %+v, want %+v", got, c.want)
			}
		})
	}
}

// Whether some probe is forced out an IPv4 gateway is read from the plans: a name is
// resolved in the gateway's family, so a name behind an IPv6 gateway is an IPv6 probe and
// must not trip the IPv4-only rp_filter warning, while one behind an IPv4 gateway does.
func TestForcesIPv4(t *testing.T) {
	for _, c := range []struct {
		name string
		spec config.Target
		want bool
	}{
		{"plain", config.Target{Name: "plain", Addr: "8.8.8.8"}, false},
		{
			"v4",
			config.Target{
				Name:   "v4",
				Addr:   "8.8.8.8",
				Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1")},
			},
			true,
		},
		{
			"name behind an IPv4 gateway",
			config.Target{
				Name:   "byname",
				Addr:   "example.com",
				Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1")},
			},
			true,
		},
		{
			"v6",
			config.Target{
				Name:   "v6",
				Addr:   "2001:db8::1",
				Params: probe.Nexthop{Gateway: netip.MustParseAddr("2001:db8::ffff")},
			},
			false,
		},
		{
			"name behind an IPv6 gateway",
			config.Target{
				Name:   "name-v6gw",
				Addr:   "example.com",
				Params: probe.Nexthop{Gateway: netip.MustParseAddr("2001:db8::ffff")},
			},
			false,
		},
		{
			"ssh",
			config.Target{
				Name:   "ssh",
				Addr:   "8.8.8.8",
				Params: probe.SSH{Host: "h", OS: probe.OSLinux},
			},
			false,
		},
	} {
		if got := forcesIPv4(planned(t, c.spec)); got != c.want {
			t.Errorf("%s: forcesIPv4 = %v, want %v", c.name, got, c.want)
		}
	}
}

// nexthopWarnings asks the host whether rp_filter is strict, which is flagged only where
// a forced probe is IPv4: an IPv6-only next-hop has no rp_filter knob to warn about. (A
// platform without forcing never builds a forced target.)
func TestNexthopWarningsHostGates(t *testing.T) {
	v4 := config.Target{
		Name:   "gw4",
		Addr:   "8.8.8.8",
		Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1")},
	}
	v6 := config.Target{
		Name:   "gw6",
		Addr:   "2001:db8::1",
		Params: probe.Nexthop{Gateway: netip.MustParseAddr("2001:db8::ffff")},
	}

	cases := []struct {
		name string
		spec config.Target
		host fakeHost
		want []Diagnostic
	}{
		{"capable_host_silent", v4, capable, nil},
		{
			"rp_filter_strict_v4",
			v4,
			fakeHost{direct: true, raw: true, rpStrict: true},
			[]Diagnostic{StrictRPFilter{}},
		},
		{
			"rp_filter_strict_v6_only",
			v6,
			fakeHost{direct: true, raw: true, rpStrict: true},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nexthopWarnings(planned(t, c.spec), c.host)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("nexthopWarnings = %+v, want %+v", got, c.want)
			}
		})
	}
}

// Every row the deprecated snmp method probes is named, in config order, in one warning;
// a config without one raises none.
func TestSNMPWarningsNameTheSNMPTargets(t *testing.T) {
	agent := probe.SNMP{Host: "agent", Community: "public"}

	got := snmpWarnings(planned(
		t,
		config.Target{Name: "direct", Addr: "192.0.2.1"},
		config.Target{Name: "a", Addr: "192.0.2.2", Params: agent},
		config.Target{
			Name:   "ssh",
			Addr:   "192.0.2.3",
			Params: probe.SSH{Host: "h", OS: probe.OSLinux},
		},
		config.Target{Name: "b", Addr: "2001:db8::4", Params: agent},
	))

	want := []Diagnostic{SNMPDeprecated{Targets: []string{"a", "b"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snmpWarnings = %+v, want %+v", got, want)
	}

	none := snmpWarnings(planned(t, config.Target{Name: "direct", Addr: "192.0.2.1"}))
	if none != nil {
		t.Errorf("snmpWarnings without snmp targets = %+v, want none", none)
	}
}

// A loaded config warns about its snmp rows at start and on every reload.
func TestBuildWarnsOfSNMPRows(t *testing.T) {
	svc := NewService(Ports{NewPinger: fakePingers(nil), Host: capable})

	_, _, loaded := svc.build(configOf(config.Target{
		Name:   "via-agent",
		Addr:   "192.0.2.1",
		Params: probe.SNMP{Host: "agent", Community: "public"},
	}))

	want := []Diagnostic{SNMPDeprecated{Targets: []string{"via-agent"}}}
	if !reflect.DeepEqual(loaded.Warnings, want) {
		t.Errorf("warnings = %+v, want %+v", loaded.Warnings, want)
	}
}
