package configfile

import (
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Equivalent spellings reach adapters with the same parameters and row identity.
func TestIPSpellingsCompileToSamePath(t *testing.T) {
	for _, c := range []struct{ written, canonical string }{
		{"008.008.008.008", "8.8.8.8"},
		{"192.000.002.001 source=192.000.002.009", "192.0.2.1 source=192.0.2.9"},
		{
			"192.000.002.001 probe=nexthop nexthop=192.000.002.254 source=192.000.002.009",
			"192.0.2.1 probe=nexthop nexthop=192.0.2.254 source=192.0.2.9",
		},
		{
			"192.000.002.001 probe=ssh relay=ops@192.000.002.009 os=Linux",
			"192.0.2.1 probe=ssh relay=192.0.2.9 user=ops os=Linux",
		},
		{
			"192.000.002.001 probe=snmp relay=192.000.002.009 community=public",
			"192.0.2.1 probe=snmp relay=192.0.2.9 community=public",
		},
		{
			"192.000.002.001 probe=routeros relay=192.000.002.009:0443 username=u password=p",
			"192.0.2.1 probe=routeros relay=192.0.2.9 username=u password=p",
		},
		{
			"2001:0DB8:0:0:0:0:0:1 probe=ssh relay=2001:0DB8:0:0:0:0:0:9 os=Linux",
			"2001:db8::1 probe=ssh relay=2001:db8::9 os=Linux",
		},
		{
			"2001:db8::1 probe=snmp relay=2001:0DB8:0:0:0:0:0:9 community=public",
			"2001:db8::1 probe=snmp relay=2001:db8::9 community=public",
		},
	} {
		cfg, err := Parse(strings.NewReader("h " + c.written + "\nh " + c.canonical))
		if err != nil {
			t.Fatal(err)
		}

		written, err := probe.Compile(target(t, cfg, 0).ProbeSpec())
		if err != nil {
			t.Fatalf("%s: %v", c.written, err)
		}

		canonical, err := probe.Compile(target(t, cfg, 1).ProbeSpec())
		if err != nil {
			t.Fatalf("%s: %v", c.canonical, err)
		}

		if written != canonical || written.Identity() != canonical.Identity() {
			t.Errorf(
				"%s and %s compiled differently: %+v / %+v",
				c.written,
				c.canonical,
				written,
				canonical,
			)
		}

		if got := target(t, cfg, 0).Addr; got != strings.Fields(c.written)[0] {
			t.Errorf("parser lost the original address: %q", got)
		}
	}
}
