package monitoring

import (
	"net/netip"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// planned compiles target lines into the probe targets the warnings see, failing the
// test when one does not compile.
func planned(t *testing.T, specs ...config.Target) []probeTarget {
	t.Helper()

	targets := make([]probeTarget, 0, len(specs))

	for _, s := range specs {
		plan, err := probe.Compile(s.ProbeSpec())
		if err != nil {
			t.Fatalf("Compile(%s): %v", s.Name, err)
		}

		targets = append(targets, probeTarget{name: s.Name, plan: plan})
	}

	return targets
}

// A row that could not be built is never probed, so its build diagnostic is the only
// warning about it: no probing warning may describe how it would be probed. The host
// kinds are one without any ICMP socket and one without raw ICMP where rp_filter is
// strict, which between them raise every probing warning.
func TestUnbuildableRowsRaiseOnlyTheirBuildDiagnostic(t *testing.T) {
	lines := []config.Line{
		config.Malformed{Name: "typo-via", Addr: "192.0.2.1", Problem: `unknown probe="via"`},
		config.Malformed{
			Name:    "typo-via-direct",
			Addr:    "192.0.2.2",
			Problem: `attribute "via" is not supported by probe=direct`,
		},
		config.Target{Name: "bad-tcp", Addr: "192.0.2.3", Params: probe.TCP{}},
		config.Target{
			Name:   "bad-gw",
			Addr:   "192.0.2.4",
			Params: probe.Nexthop{Gateway: netip.Addr{}},
		},
		config.Target{
			Name:   "mismatch",
			Addr:   "2001:db8::1",
			Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.254")},
		},
		config.Target{
			Name:   "bad-verify",
			Addr:   "192.0.2.5",
			Params: probe.QUIC{Verify: probe.Verification(99)},
		},
		config.Target{
			Name:   "bad-snmp",
			Addr:   "fe80::1%eth0",
			Params: probe.SNMP{Host: "agent", Community: "public"},
		},
	}
	names := []string{
		"typo-via", "typo-via-direct", "bad-tcp", "bad-gw", "mismatch", "bad-verify", "bad-snmp",
	}

	for name, host := range map[string]fakeHost{
		"no ICMP socket":           {},
		"strict rp_filter, no raw": {rpStrict: true},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewService(Ports{NewPinger: fakePingers(nil), Host: host})

			rows, _, loaded := svc.build(configOf(lines...))
			warns := loaded.Warnings

			if len(rows) != 0 {
				t.Fatalf("unexpected active rows: %+v", rows)
			}

			if len(warns) != len(names) {
				t.Fatalf("warnings = %+v, want only the %d build diagnostics", warns, len(names))
			}

			for i, w := range warns {
				if failed, ok := w.(TargetBuildFailed); !ok || failed.Name != names[i] {
					t.Errorf(
						"warning %d = %+v, want the build diagnostic of %s",
						i,
						w,
						names[i],
					)
				}
			}
		})
	}
}

// A row whose adapter could not be built is not probed either, though its plan compiled.
func TestAdapterFailureRaisesOnlyItsBuildDiagnostic(t *testing.T) {
	svc := NewService(Ports{NewPinger: fakePingers(&[]string{"192.0.2.1"}), Host: fakeHost{}})

	_, _, loaded := svc.build(configOf(
		config.Target{
			Name:   "direct",
			Addr:   "192.0.2.1",
			Params: probe.Direct{Source: probe.SourceInterface("eth0")},
		},
	))
	if _, ok := onlyWarning[TargetBuildFailed](loaded.Warnings); !ok {
		t.Fatalf("warnings = %+v, want only the build diagnostic", loaded.Warnings)
	}
}

// The source= a method ignores is named with its target.
