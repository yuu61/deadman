package ping

import "testing"

// resolveNetwork maps the resolve_family attribute to a pro-bing network string.
// Only the spellings registered in resolveFamilies are recognized (case-sensitive,
// like the via/relay values); anything else — unset, a typo, an uppercase spelling,
// or pro-bing's own "ip4"/"ip6" — falls back to "" so resolution stays automatic.
func TestResolveNetwork(t *testing.T) {
	// Recognized spellings are driven by the registry itself, the single source of
	// truth: every entry must round-trip through resolveNetwork to its network string.
	for value, want := range resolveFamilies {
		t.Run(value, func(t *testing.T) {
			s := Spec{Relay: map[string]string{"resolve_family": value}}
			if got := resolveNetwork(s); got != want {
				t.Errorf("resolveNetwork(resolve_family=%q) = %q, want %q", value, got, want)
			}
		})
	}

	// Everything outside the registry resolves to "" (auto): a typo, an uppercase
	// spelling, pro-bing's own network spelling, and an explicit empty value.
	for _, value := range []string{"ipv5", "IPV4", "ip4", ""} {
		t.Run("auto/"+value, func(t *testing.T) {
			s := Spec{Relay: map[string]string{"resolve_family": value}}
			if got := resolveNetwork(s); got != "" {
				t.Errorf("resolveNetwork(resolve_family=%q) = %q, want \"\"", value, got)
			}
		})
	}

	// A nil relay map (no attributes at all) must not panic.
	if got := resolveNetwork(Spec{}); got != "" {
		t.Errorf("resolveNetwork(no relay) = %q, want \"\"", got)
	}
}

// TestNewICMPPingerNetwork pins the Spec->pinger wiring: newICMPPinger must carry
// the resolved network onto the pinger, or the family is silently never applied and
// every other test still passes. Whitebox so it needs no real resolution.
func TestNewICMPPingerNetwork(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"pinned", Spec{Relay: map[string]string{"resolve_family": "ipv6"}}, networkIPv6},
		{"auto", Spec{Relay: map[string]string{}}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := newICMPPinger(c.spec)
			if err != nil {
				t.Fatalf("newICMPPinger: %v", err)
			}

			ip, ok := p.(*icmpPinger)
			if !ok {
				t.Fatalf("newICMPPinger returned %T, want *icmpPinger", p)
			}

			if got := ip.network; got != c.want {
				t.Errorf("icmpPinger.network = %q, want %q", got, c.want)
			}
		})
	}
}
