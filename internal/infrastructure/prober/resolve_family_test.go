package prober

import (
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// resolveNetwork maps a plan's resolve family to the network string that pins it; the
// resolver's choice (FamilyUnknown) pins nothing.
func TestResolveNetwork(t *testing.T) {
	cases := map[probe.Family]string{
		probe.FamilyIPv4:    networkIPv4,
		probe.FamilyIPv6:    networkIPv6,
		probe.FamilyUnknown: "",
	}
	for family, want := range cases {
		if got := resolveNetwork(family); got != want {
			t.Errorf("resolveNetwork(%v) = %q, want %q", family, got, want)
		}
	}
}

func TestNewICMPPingerNetwork(t *testing.T) {
	for family, want := range map[probe.Family]string{probe.FamilyUnknown: "", probe.FamilyIPv6: "ip6"} {
		p, err := New(
			compiled(
				t,
				probe.Spec{Addr: "example.com", Params: probe.Direct{Family: family}},
			),
			"row#1",
		)
		if err != nil {
			t.Fatal(err)
		}

		ip, ok := p.(*icmpPinger)
		if !ok || ip.network != want {
			t.Fatalf("family %v: %+v", family, p)
		}
	}
}
