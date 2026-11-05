package probe

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestEchoOptionsRejectInvalidConditions(t *testing.T) {
	for _, params := range []Params{
		Direct{Echo: EchoOptions{Size: PayloadBytes(-1)}},
		Direct{Echo: EchoOptions{Size: PayloadBytes(0)}},
		Direct{Echo: EchoOptions{Size: PayloadBytes(23)}},
		Direct{Echo: EchoOptions{Size: PayloadBytes(65508)}},
		SSH{Host: "r", OS: OSLinux, Echo: EchoOptions{Size: PayloadBytes(-1)}},
		Netns{Name: "ns", Echo: EchoOptions{Size: PayloadBytes(65508)}},
		VRF{Name: "v", Echo: EchoOptions{Size: PayloadBytes(-1)}},
		SSH{Host: "r", OS: OSLinux, Interface: "eth0"},
		SSH{Host: "r", OS: OSDarwin, Instance: "MGMT"},
		SSH{Host: "r", OS: OSJUNOS, Source: SourceInterface("ge-0/0/0.0")},
		SSH{Host: "r", OS: OSJUNOS, Interface: "ge-0/0/0.0 | save /tmp/a"},
		SSH{Host: "r", OS: OSJUNOS, Instance: "MGMT\x00"},
		SSH{Host: "r", OS: OSJUNOS, Instance: "$(reboot)"},
		SSH{Host: "r", OS: OSJUNOS, Timeout: TimeoutAfter(0)},
		SSH{Host: "r", OS: OSLinux, Timeout: TimeoutAfter(-time.Second)},
	} {
		_, err := Compile(Spec{Addr: "192.0.2.1", Params: params})
		if err == nil {
			t.Errorf("Compile accepted invalid conditions: %+v", params)
		}
	}
}

func TestRelayPayloadRanges(t *testing.T) {
	for _, relayOS := range []OS{OSLinux, OSDarwin, OSFreeBSD, OSJUNOS} {
		for _, addr := range []string{"192.0.2.1", "2001:db8::1"} {
			for _, size := range []int{-1, 0, 1, 56, 65468, 65469, 65507, 65508} {
				params := SSH{Host: "r", OS: relayOS, Echo: EchoOptions{Size: PayloadBytes(size)}}
				_, err := Compile(Spec{Addr: addr, Params: params})

				valid := size >= 0 && size <= 65507
				if relayOS == OSJUNOS {
					valid = size >= 0 && size <= 65468
				}

				if (relayOS == OSDarwin || relayOS == OSFreeBSD) && addr == "2001:db8::1" &&
					size == 0 {
					valid = false
				}

				if (err == nil) != valid {
					t.Errorf(
						"%s %s size=%d: error=%v, want valid=%v",
						relayOS,
						addr,
						size,
						err,
						valid,
					)
				}
			}
		}
	}

	// A hostname's family is settled by Compile before applying the dialect's limits.
	for _, params := range []SSH{
		{Host: "r", OS: OSFreeBSD, Family: FamilyIPv6},
		{Host: "r", OS: OSDarwin, Source: SourceAddr(netip.MustParseAddr("2001:db8::2"))},
	} {
		params.Echo.Size = PayloadBytes(0)

		_, err := Compile(Spec{Addr: "target.example", Params: params})
		if err == nil {
			t.Errorf("accepted zero IPv6 payload: %+v", params)
		}
	}
}

func TestJUNOSSourceAndFamily(t *testing.T) {
	plan := mustCompile(t, Spec{Addr: "router.example", Params: SSH{
		Host:      "r",
		OS:        OSJUNOS,
		Source:    SourceAddr(netip.MustParseAddr("2001:db8::2")),
		Interface: "ge-0/0/0.0",
		Instance:  "MGMT",
		Echo:      EchoOptions{Size: PayloadBytes(8952), DF: true},
	}})

	ssh, ok := plan.Params().(SSH)
	if !ok || ssh.Family != FamilyIPv6 || ssh.Source.String() != "2001:db8::2" {
		t.Fatalf("resolved parameters = %+v", plan.Params())
	}

	_, err := Compile(Spec{Addr: "router.example", Params: SSH{Host: "r", OS: OSJUNOS}})
	if err == nil || !strings.Contains(err.Error(), "resolve_family") {
		t.Fatalf("unresolved relay hostname = %v", err)
	}
}

func TestEchoDefaultsPreserveIdentity(t *testing.T) {
	for _, pair := range [][2]Params{
		{Direct{}, Direct{Echo: EchoOptions{Size: PayloadBytes(24)}}},
		{SSH{Host: "r", OS: OSLinux}, SSH{
			Host: "r", OS: OSLinux, Echo: EchoOptions{Size: PayloadBytes(56)}, Timeout: TimeoutAfter(5 * time.Second),
		}},
		{SSH{Host: "r", OS: OSJUNOS}, SSH{
			Host: "r", OS: OSJUNOS, Echo: EchoOptions{Size: PayloadBytes(56)}, Timeout: TimeoutAfter(3 * time.Second),
		}},
		{Netns{Name: "ns"}, Netns{Name: "ns", Echo: EchoOptions{Size: PayloadBytes(56)}}},
		{VRF{Name: "vrf"}, VRF{Name: "vrf", Echo: EchoOptions{Size: PayloadBytes(56)}}},
	} {
		left := mustCompile(t, Spec{Addr: "192.0.2.1", Params: pair[0]})

		right := mustCompile(t, Spec{Addr: "192.0.2.1", Params: pair[1]})
		if left.Identity() != right.Identity() {
			t.Errorf("default changed identity: %q vs %q", left.Identity(), right.Identity())
		}
	}

	left := mustCompile(t, Spec{Addr: "2001:db8::1", Params: SSH{Host: "r", OS: OSJUNOS}})

	right := mustCompile(t, Spec{Addr: "2001:db8::1", Params: SSH{
		Host: "r", OS: OSJUNOS, Echo: EchoOptions{DF: true},
	}})
	if left.Identity() != right.Identity() {
		t.Error("JUNOS IPv6 ignores DF, so its identity must not change")
	}
}

func TestEchoIdentitySeparatesConditions(t *testing.T) {
	cases := []Params{
		Direct{},
		Direct{Echo: EchoOptions{Size: PayloadBytes(8972)}},
		Direct{Echo: EchoOptions{DF: true}},
		SSH{Host: "r", OS: OSJUNOS},
		SSH{Host: "r", OS: OSJUNOS, Echo: EchoOptions{Size: PayloadBytes(8972)}},
		SSH{Host: "r", OS: OSJUNOS, Echo: EchoOptions{DF: true}},
		SSH{Host: "r", OS: OSJUNOS, Interface: "ge-0/0/0.0"},
		SSH{Host: "r", OS: OSJUNOS, Instance: "MGMT"},
		SSH{Host: "r", OS: OSJUNOS, Timeout: TimeoutAfter(10 * time.Second)},
		Netns{Name: "ns"},
		Netns{Name: "ns", Echo: EchoOptions{Size: PayloadBytes(0)}},
		Netns{Name: "ns", Echo: EchoOptions{DF: true}},
		VRF{Name: "v"},
		VRF{Name: "v", Echo: EchoOptions{Size: PayloadBytes(8972)}},
	}
	seen := make(map[string]bool)

	for _, params := range cases {
		id := mustCompile(t, Spec{Addr: "192.0.2.1", Params: params}).Identity()
		if seen[id] {
			t.Errorf("conditions share identity: %s", id)
		}

		seen[id] = true
	}
}
