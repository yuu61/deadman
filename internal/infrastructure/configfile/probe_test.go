package configfile

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestParseEveryMethodIntoTypedConditions(t *testing.T) {
	cases := map[probe.Method]string{
		probe.MethodDirect:   "source=eth0",
		probe.MethodTCP:      "probe=tcp port=80",
		probe.MethodQUIC:     "probe=quic port=8443 alpn=h3 sni=example.com verify=yes",
		probe.MethodSSH:      "probe=ssh relay=jump os=Darwin user=admin key=/key",
		probe.MethodNetns:    "probe=netns relay=ns",
		probe.MethodVRF:      "probe=vrf relay=blue",
		probe.MethodSNMP:     "probe=snmp relay=agent community=public",
		probe.MethodRouterOS: "probe=routeros relay=router username=user password=pass scheme=https verify=off",
		probe.MethodNexthop:  "probe=nexthop nexthop=192.0.2.254 source=eth0",
	}
	for _, method := range probe.Methods() {
		text, exists := cases[method]
		if !exists {
			t.Fatalf("no parser sample for %s", method)
		}

		cfg, err := Parse(strings.NewReader("host 192.0.2.1 " + text))
		if err != nil {
			t.Fatal(err)
		}

		row := target(t, cfg, 0)

		plan, err := probe.Compile(row.ProbeSpec())
		if err != nil || plan.Method() != method {
			t.Fatalf("%s: %+v, %v", method, plan, err)
		}
	}
}

// Every attribute is written with a value or not at all: "key=" would otherwise take the
// method's default (alpn= becoming h3, scheme= https) or vanish (source=), so it is refused.
func TestParseRefusesEmptyValues(t *testing.T) {
	for _, attrs := range []string{
		"source=",
		"probe=",
		"probe=quic alpn=",
		"probe=quic sni=",
		`probe=quic sni=""`,
		"probe=quic port=",
		"probe=routeros relay=r username=u password=p scheme=",
		"probe=ssh relay=jump os=Linux user=",
	} {
		cfg, err := Parse(strings.NewReader("h 192.0.2.1 " + attrs))
		if err != nil {
			t.Fatal(err)
		}

		if p := malformed(t, cfg, 0).Problem; !strings.Contains(p, "needs a value") {
			t.Errorf("%s: problem = %q, want an empty-value problem", attrs, p)
		}
	}
}

// The parser reads a port as an integer and leaves its range to Compile: a written 0 is
// an invalid port there, not an omitted one that takes QUIC's 443.
func TestParseLeavesPortRangeToCompile(t *testing.T) {
	cfg, err := Parse(strings.NewReader("h 192.0.2.1 probe=quic port=0"))
	if err != nil {
		t.Fatal(err)
	}

	row := target(t, cfg, 0)
	if params[probe.QUIC](t, row.Params).Port != probe.PortNumber(0) {
		t.Fatalf("row = %+v", row)
	}

	_, err = probe.Compile(row.ProbeSpec())
	if err == nil || !strings.Contains(err.Error(), "invalid port 0") {
		t.Errorf("Compile(port=0) = %v", err)
	}
}

// source= is an address when it parses as one — a zoned IPv6 address included — and an
// interface name otherwise; the adapters never classify the text again.
func TestParseSourceKinds(t *testing.T) {
	for text, want := range map[string]probe.Source{
		"10.0.0.1":        probe.SourceAddr(netip.MustParseAddr("10.0.0.1")),
		"010.000.000.001": probe.SourceAddr(netip.MustParseAddr("10.0.0.1")),
		"fe80::1%eth0":    probe.SourceAddr(netip.MustParseAddr("fe80::1%eth0")),
		"eth0":            probe.SourceInterface("eth0"),
		"10.0.0.300":      probe.SourceInterface("10.0.0.300"),
	} {
		cfg, err := Parse(strings.NewReader("h example.com source=" + text))
		if err != nil {
			t.Fatal(err)
		}

		if got := params[probe.Direct](t, target(t, cfg, 0).Params).Source; got != want {
			t.Errorf("source=%s parsed as %+v, want %+v", text, got, want)
		}
	}
}

// An omitted nexthop= is Compile's to name as missing, not a malformed empty address.
func TestParseLeavesMissingGatewayToCompile(t *testing.T) {
	cfg, err := Parse(strings.NewReader("h 192.0.2.1 probe=nexthop"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = probe.Compile(target(t, cfg, 0).ProbeSpec())
	if err == nil || !strings.Contains(err.Error(), "requires an IP gateway") {
		t.Errorf("Compile = %v", err)
	}
}

// routeros's relay= may carry a port. The parser splits the host from the port and reads
// the port as an integer; Compile then checks both, so a port out of range is Compile's
// error and a malformed spelling the parser's.
func TestParseRouterOSRelay(t *testing.T) {
	parse := func(relay string) config.Line {
		t.Helper()

		cfg, err := Parse(strings.NewReader(
			"h 192.0.2.1 probe=routeros username=u password=p relay=" + relay,
		))
		if err != nil {
			t.Fatal(err)
		}

		return cfg.Lines[0]
	}

	for relay, want := range map[string]probe.RouterOS{
		"router":                 {Host: "router"},
		"router:8443":            {Host: "router", Port: probe.PortNumber(8443)},
		"router:080":             {Host: "router", Port: probe.PortNumber(80)},
		"192.0.2.9:8443":         {Host: "192.0.2.9", Port: probe.PortNumber(8443)},
		"192.000.002.009:8443":   {Host: "192.000.002.009", Port: probe.PortNumber(8443)},
		"[192.000.002.009]:8443": {Host: "192.000.002.009", Port: probe.PortNumber(8443)},
		"2001:db8::1":            {Host: "2001:db8::1"},
		"fe80::1%ether1":         {Host: "fe80::1%ether1"},
		"[2001:db8::1]":          {Host: "2001:db8::1"},
		"[2001:db8::1]:8443":     {Host: "2001:db8::1", Port: probe.PortNumber(8443)},
		"[fe80::1%ether1]:8080":  {Host: "fe80::1%ether1", Port: probe.PortNumber(8080)},
		"router:0":               {Host: "router", Port: probe.PortNumber(0)},
	} {
		row, ok := parse(relay).(config.Target)
		if !ok {
			t.Errorf("relay=%s: not a target line", relay)

			continue
		}

		got := params[probe.RouterOS](t, row.Params)
		if got.Host != want.Host || got.Port != want.Port {
			t.Errorf(
				"relay=%s: host %q port %v, want %q %v",
				relay,
				got.Host,
				got.Port,
				want.Host,
				want.Port,
			)
		}
	}

	// A port with no host is no relay, and a port after a zone would be read as part of
	// the interface name: each is the parser's error, not a missing or odd relay.
	for _, relay := range []string{
		"router:", "router:abc", "[::1", "[router]:80", "[router]", "a:b:c", ":8443", "fe80::1%eth0:8443",
	} {
		if _, ok := parse(relay).(config.Malformed); !ok {
			t.Errorf("relay=%s: parsed, want a malformed line", relay)
		}
	}
}

// Unknown attributes, the attributes of the retired via=/tcp= syntax, attributes the
// method does not use and malformed values are construction errors: netns= is not an
// attribute (the netns method is probe=netns relay=NAME), so it must not be swallowed.
func TestParseConfigRejectsUnknownAndLegacyAttributes(t *testing.T) {
	for _, attrs := range []string{
		"netns=ns1 probe=netns",
		"via=quic",
		"tcp=dstport:80",
		"relay=jump",
		"probe=quic source=eth0",
		"probe=quic verify=maybe",
		"probe=quic port=abc",
		"probe=quic port=443 port=8443",
		"probe=unknown",
		"probe=direct nexthop=192.0.2.1",
	} {
		cfg, err := Parse(strings.NewReader("h 192.0.2.1 " + attrs))
		if err != nil {
			t.Fatal(err)
		}

		if _, ok := cfg.Lines[0].(config.Malformed); !ok {
			t.Errorf("accepted %s: %+v", attrs, cfg.Lines[0])
		}
	}
}
