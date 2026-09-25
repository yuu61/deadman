package tui

import (
	"slices"
	"testing"

	"github.com/yuu61/deadman/internal/config"
)

func TestClassifyNexthop(t *testing.T) {
	specs := []config.TargetSpec{
		{
			Name: "plain",
			Addr: "8.8.8.8",
		}, // no nexthop: skipped.
		{
			Name:  "v4",
			Addr:  "8.8.8.8",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // forced.
		{
			Name:  "byname",
			Addr:  "example.com",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // forced (unresolved name).
		{
			Name:  "v6",
			Addr:  "2001:db8::1",
			Relay: map[string]string{"nexthop": "2001:db8::ffff"},
		}, // forced (IPv6 gateway matches IPv6 target).
		{
			Name:  "mismatch",
			Addr:  "2001:db8::1",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // familyMismatch (IPv6 target, IPv4 gateway).
		{
			Name:  "ssh",
			Addr:  "8.8.8.8",
			Relay: map[string]string{"nexthop": "192.0.2.1", "relay": "h"},
		}, // modeIgnored.
		{
			Name:  "tcp",
			Addr:  "8.8.8.8",
			TCP:   "dstport:80",
			Relay: map[string]string{"nexthop": "192.0.2.1"},
		}, // modeIgnored.
	}

	c := classifyNexthop(specs)

	if want := []string{"ssh", "tcp"}; !slices.Equal(c.modeIgnored, want) {
		t.Errorf("modeIgnored = %v, want %v", c.modeIgnored, want)
	}

	if want := []string{"mismatch"}; !slices.Equal(c.familyMismatch, want) {
		t.Errorf("familyMismatch = %v, want %v", c.familyMismatch, want)
	}

	if want := []string{"v4", "byname", "v6"}; !slices.Equal(c.forced, want) {
		t.Errorf("forced = %v, want %v", c.forced, want)
	}

	if !c.forcedV4 {
		t.Error("forcedV4 = false, want true (IPv4 and name targets are present)")
	}
}

// TestClassifyNexthopViaPrecedence pins classifyOne to ping's selectMethod precedence
// rather than a hand-rolled "any via= wins" check. A recognized via= (e.g. snmp) does
// take precedence, so the gateway is genuinely ignored; an unrecognized via= falls
// through to nexthop in selectMethod, so the gateway IS honored and must not be
// reported as ignored.
func TestClassifyNexthopViaPrecedence(t *testing.T) {
	// Recognized via=: a real relay mode wins, so the next-hop is ignored.
	if c := classifyNexthop([]config.TargetSpec{{
		Name:  "via-snmp",
		Addr:  "8.8.8.8",
		Relay: map[string]string{"nexthop": "192.0.2.1", "via": "snmp"},
	}}); !slices.Equal(c.modeIgnored, []string{"via-snmp"}) {
		t.Errorf("recognized via=snmp: modeIgnored = %v, want [via-snmp]", c.modeIgnored)
	}

	// Unrecognized via=: selectMethod falls through to nexthop, so the gateway is
	// honored. The old inline `s.Relay["via"] != ""` check wrongly flagged this as
	// ignored — the drift this delegation fixes.
	c := classifyNexthop([]config.TargetSpec{{
		Name:  "via-bogus",
		Addr:  "8.8.8.8",
		Relay: map[string]string{"nexthop": "192.0.2.1", "via": "bogus"},
	}})

	if len(c.modeIgnored) != 0 {
		t.Errorf("unrecognized via=bogus: modeIgnored = %v, want none", c.modeIgnored)
	}

	if !slices.Equal(c.forced, []string{"via-bogus"}) {
		t.Errorf("unrecognized via=bogus: forced = %v, want [via-bogus]", c.forced)
	}
}

func TestClassifyNexthopForcedV4Scope(t *testing.T) {
	// A name is resolved to the gateway's family at runtime, so a name behind an IPv6
	// gateway is an IPv6 probe and must NOT trip the IPv4-only rp_filter warning.
	c := classifyNexthop([]config.TargetSpec{{
		Name:  "name-v6gw",
		Addr:  "example.com",
		Relay: map[string]string{"nexthop": "2001:db8::ffff"},
	}})

	if want := []string{"name-v6gw"}; !slices.Equal(c.forced, want) {
		t.Errorf("forced = %v, want %v", c.forced, want)
	}

	if c.forcedV4 {
		t.Error("forcedV4 = true for a name behind an IPv6 gateway; want false")
	}

	// A name behind an IPv4 gateway stays rp_filter-relevant.
	if c := classifyNexthop([]config.TargetSpec{{
		Name:  "name-v4gw",
		Addr:  "example.com",
		Relay: map[string]string{"nexthop": "192.0.2.1"},
	}}); !c.forcedV4 {
		t.Error("forcedV4 = false for a name behind an IPv4 gateway; want true")
	}
}
