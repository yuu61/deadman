package probe

import (
	"net/netip"
	"testing"
)

// A written target is an address or a name, decided once: an IP literal (a zoned or
// IPv4-mapped one included) is an address of its family, anything else a name.
func TestDestinationKinds(t *testing.T) {
	cases := map[string]Family{
		"192.0.2.1":        FamilyIPv4,
		"::ffff:192.0.2.1": FamilyIPv4,
		"2001:db8::1":      FamilyIPv6,
		"fe80::1%eth0":     FamilyIPv6,
		"example.com":      FamilyUnknown,
	}
	for addr, want := range cases {
		d, err := parseDestination(addr)
		if err != nil {
			t.Fatal(err)
		}

		if got := d.Family(); got != want {
			t.Errorf("%q: family %v, want %v", addr, got, want)
		}

		_, isIP := d.IP()
		_, isName := d.Name()

		if isIP == (want == FamilyUnknown) || isName == isIP {
			t.Errorf("%q: IP %v, name %v", addr, isIP, isName)
		}
	}

	// A zoned IPv6 address keeps its zone; the adapters send out that interface.
	d, err := parseDestination("fe80::1%eth0")
	if a, _ := d.IP(); err != nil || a != netip.MustParseAddr("fe80::1%eth0") {
		t.Errorf("zoned destination = %v, %v", a, err)
	}
}
