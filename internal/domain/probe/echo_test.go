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
		SSH{Host: "r", OS: OSLinux, Interface: CLIWordOf("eth0")},
		SSH{Host: "r", OS: OSDarwin, Instance: CLIWordOf("MGMT")},
		SSH{Host: "r", OS: OSJUNOS, Source: SourceInterface("ge-0/0/0.0")},
		SSH{Host: "r", OS: OSJUNOS, Interface: CLIWordOf("ge-0/0/0.0 | save /tmp/a")},
		SSH{Host: "r", OS: OSJUNOS, Instance: CLIWordOf("MGMT\x00")},
		SSH{Host: "r", OS: OSJUNOS, Interface: CLIWordOf("")},
		SSH{Host: "r", OS: OSJUNOS, Instance: CLIWordOf("")},
		SSH{Host: "r", OS: OSJUNOS, Interface: CLIWordOf("-ge-0/0/0.0")},
		SSH{Host: "r", OS: OSJUNOS, Instance: CLIWordOf("-MGMT")},
		SSH{Host: "r", OS: OSJUNOS, Instance: CLIWordOf("$(reboot)")},
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
			for _, size := range []int{-1, 0, 1, 8, 15, 16, 56, 65468, 65469, 65507, 65508} {
				params := SSH{Host: "r", OS: relayOS, Echo: EchoOptions{Size: PayloadBytes(size)}}
				_, err := Compile(Spec{Addr: addr, Params: params})

				valid := size >= RelayMinPayload && size <= 65507
				if relayOS == OSJUNOS {
					valid = size >= RelayMinPayload && size <= 65468
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
		params.Echo.Size = PayloadBytes(RelayMinPayload - 1)

		_, err := Compile(Spec{Addr: "target.example", Params: params})
		if err == nil {
			t.Errorf("accepted a payload too small to time: %+v", params)
		}
	}

	// netns and vrf run the same Linux ping as an ssh relay does.
	for _, size := range []int{0, RelayMinPayload - 1} {
		for _, params := range []Params{
			Netns{Name: "ns", Echo: EchoOptions{Size: PayloadBytes(size)}},
			VRF{Name: "v", Echo: EchoOptions{Size: PayloadBytes(size)}},
		} {
			_, err := Compile(Spec{Addr: "192.0.2.1", Params: params})
			if err == nil {
				t.Errorf("accepted a payload too small to time: %+v", params)
			}
		}
	}
}

func TestJUNOSSourceAndFamily(t *testing.T) {
	plan := mustCompile(t, Spec{Addr: "router.example", Params: SSH{
		Host:      "r",
		OS:        OSJUNOS,
		Source:    SourceAddr(netip.MustParseAddr("2001:db8::2")),
		Interface: CLIWordOf("ge-0/0/0.0"),
		Instance:  CLIWordOf("MGMT"),
		Echo:      EchoOptions{Size: PayloadBytes(8952)},
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
			Host: "r", OS: OSJUNOS, Echo: EchoOptions{Size: PayloadBytes(56)}, Timeout: TimeoutAfter(5 * time.Second),
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
}

// Junos OS Evolved cannot send an IPv6 echo with do-not-fragment, so the row is refused
// instead of probing a path that may fragment; df=off and IPv4 DF still compile.
func TestJUNOSIPv6RefusesDF(t *testing.T) {
	_, err := Compile(Spec{Addr: "2001:db8::1", Params: SSH{
		Host: "r", OS: OSJUNOS, Echo: EchoOptions{DF: true},
	}})
	if err == nil || !strings.Contains(err.Error(), "df=on") {
		t.Errorf("JUNOS IPv6 df=on = %v, want a build error", err)
	}

	mustCompile(t, Spec{Addr: "2001:db8::1", Params: SSH{Host: "r", OS: OSJUNOS}})
	mustCompile(t, Spec{Addr: "192.0.2.1", Params: SSH{
		Host: "r", OS: OSJUNOS, Echo: EchoOptions{DF: true},
	}})
}

func TestEchoIdentitySeparatesConditions(t *testing.T) {
	cases := []Params{
		Direct{},
		Direct{Echo: EchoOptions{Size: PayloadBytes(8972)}},
		Direct{Echo: EchoOptions{DF: true}},
		SSH{Host: "r", OS: OSJUNOS},
		SSH{Host: "r", OS: OSJUNOS, Echo: EchoOptions{Size: PayloadBytes(8972)}},
		SSH{Host: "r", OS: OSJUNOS, Echo: EchoOptions{DF: true}},
		SSH{Host: "r", OS: OSJUNOS, Interface: CLIWordOf("ge-0/0/0.0")},
		SSH{Host: "r", OS: OSJUNOS, Instance: CLIWordOf("MGMT")},
		Netns{Name: "ns"},
		Netns{Name: "ns", Echo: EchoOptions{Size: PayloadBytes(RelayMinPayload)}},
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

// A deadline does not change which path is monitored, so tuning it keeps the row's history
// and log file.
func TestTimeoutDoesNotChangeIdentity(t *testing.T) {
	for _, params := range [][2]Params{
		{SSH{Host: "r", OS: OSLinux}, SSH{Host: "r", OS: OSLinux, Timeout: TimeoutAfter(30 * time.Second)}},
		{SSH{Host: "r", OS: OSJUNOS}, SSH{Host: "r", OS: OSJUNOS, Timeout: TimeoutAfter(500 * time.Millisecond)}},
	} {
		left := mustCompile(t, Spec{Addr: "192.0.2.1", Params: params[0]})

		right := mustCompile(t, Spec{Addr: "192.0.2.1", Params: params[1]})
		if left.Identity() != right.Identity() {
			t.Errorf("timeout changed identity: %q vs %q", left.Identity(), right.Identity())
		}
	}
}
