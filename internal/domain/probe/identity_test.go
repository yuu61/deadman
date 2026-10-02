package probe

import (
	"net/netip"
	"testing"
)

// Identity spellings name existing logs; equality between two generated identities
// alone would allow both to change together and silently switch log files.
func TestIdentitySpellingContract(t *testing.T) {
	for _, c := range []struct {
		name   string
		params Params
		want   string
	}{
		{"direct", Direct{Source: SourceInterface("eth0")}, `"direct":"192.0.2.1":"eth0":"1":`},
		{"tcp", TCP{Port: PortNumber(443)}, `"tcp":"192.0.2.1":"443":"1":`},
		{"snmp", SNMP{Host: "agent", Community: "secret"}, `"snmp":"192.0.2.1":"agent":`},
		{
			"netns",
			Netns{Name: "prod", Source: SourceInterface("eth1")},
			`"netns":"192.0.2.1":"prod":"eth1":"1":`,
		},
		{"vrf", VRF{Name: "blue"}, `"vrf":"192.0.2.1":"blue":"":"1":`},
		{
			"routeros",
			RouterOS{
				Host: "router", Scheme: "http", Port: PortNumber(8080),
				Username: "user", Password: "secret", Verify: VerifyDisabled,
			},
			`"routeros":"192.0.2.1":"router":"http":"8080":"noverify":`,
		},
		{
			"quic",
			QUIC{Port: PortNumber(8443), ALPN: "hq", SNI: "example.com", Verify: VerifyEnabled},
			`"quic":"192.0.2.1":"8443":"1":"hq":"example.com":"verify":`,
		},
		{
			"ssh",
			SSH{
				Host: "ops@jump", OS: OSLinux, Key: "secret",
				Source: SourceAddr(netip.MustParseAddr("192.0.2.9")),
			},
			`"ssh":"192.0.2.1":"jump":"192.0.2.9":"Linux":"1":`,
		},
		{
			"nexthop",
			Nexthop{Gateway: netip.MustParseAddr("192.0.2.254"), Source: SourceInterface("eth1")},
			`"nexthop":"192.0.2.1":"192.0.2.254":"eth1":`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := mustCompile(t, Spec{Addr: "192.0.2.1", Params: c.params})
			if got := plan.Identity(); got != c.want {
				t.Fatalf("Identity = %q, want %q", got, c.want)
			}
		})
	}
}
