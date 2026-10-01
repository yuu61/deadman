package prober

import (
	"errors"
	"net/netip"
	"runtime"
	"slices"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// The ping binary is chosen by the target's OS, not by probing a local ping6 (which
// is irrelevant to an ssh remote): Linux and FreeBSD use the unified `ping -6`, macOS its
// ping6. A name gets -4 where the ping would otherwise pick the family itself, so
// resolve_family holds; an IPv4 literal needs no flag.
func TestPingCommand(t *testing.T) {
	cases := []struct {
		name  string
		ipv   probe.Family
		os    probe.OS
		named bool
		want  []string
	}{
		{
			"ipv4_linux",
			probe.FamilyIPv4,
			probe.OSLinux,
			false,
			[]string{cmdPing, "-c", "1", "-W", "1"},
		},
		{
			"ipv4_linux_name",
			probe.FamilyIPv4,
			probe.OSLinux,
			true,
			[]string{cmdPing, "-4", "-c", "1", "-W", "1"},
		},
		{
			"ipv4_darwin",
			probe.FamilyIPv4,
			probe.OSDarwin,
			false,
			[]string{cmdPing, "-c", "1", "-W", "1000"},
		},
		{
			"ipv4_darwin_name",
			probe.FamilyIPv4,
			probe.OSDarwin,
			true,
			[]string{cmdPing, "-c", "1", "-W", "1000"},
		},
		{
			"ipv4_freebsd",
			probe.FamilyIPv4,
			probe.OSFreeBSD,
			false,
			[]string{cmdPing, "-c", "1", "-W", "1000"},
		},
		{
			"ipv4_freebsd_name",
			probe.FamilyIPv4,
			probe.OSFreeBSD,
			true,
			[]string{cmdPing, "-4", "-c", "1", "-W", "1000"},
		},
		{
			"ipv6_linux",
			probe.FamilyIPv6,
			probe.OSLinux,
			false,
			[]string{cmdPing, "-6", "-c", "1", "-W", "1"},
		},
		{
			"ipv6_freebsd",
			probe.FamilyIPv6,
			probe.OSFreeBSD,
			true,
			[]string{cmdPing, "-6", "-c", "1", "-W", "1000"},
		},
		{"ipv6_darwin", probe.FamilyIPv6, probe.OSDarwin, false, []string{cmdPing6, "-c", "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pingCommand(c.ipv, c.os, c.named); !slices.Equal(got, c.want) {
				t.Errorf("pingCommand(%d, %q, %v) = %v, want %v", c.ipv, c.os, c.named, got, c.want)
			}
		})
	}
}

// compiled compiles s or fails the test: adapters are built from plans only.
func compiled(t *testing.T, s probe.Spec) probe.Plan {
	t.Helper()

	plan, err := probe.Compile(s)
	if err != nil {
		t.Fatalf("Compile(%+v): %v", s, err)
	}

	return plan
}

// A plan of every method builds through the exhaustive parameter dispatch.
func TestEveryMethodBuildsItsCompiledParams(t *testing.T) {
	specs := []probe.Spec{
		{Addr: "192.0.2.1"},
		{Addr: "192.0.2.1", Params: probe.TCP{Port: probe.PortNumber(80)}},
		{
			Addr:   "192.0.2.1",
			Params: probe.SNMP{Host: "agent", Community: "c"},
		},
		{Addr: "192.0.2.1", Params: probe.Netns{Name: "ns"}},
		{Addr: "192.0.2.1", Params: probe.VRF{Name: "v"}},
		{
			Addr:   "192.0.2.1",
			Params: probe.RouterOS{Host: "r", Username: "u", Password: "p"},
		},
		{Addr: "192.0.2.1", Params: probe.QUIC{}},
		{
			Addr:   "192.0.2.1",
			Params: probe.SSH{Host: "jump", OS: probe.OSLinux},
		},
		{
			Addr:   "192.0.2.1",
			Params: probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.254")},
		},
	}

	covered := map[probe.Method]bool{}

	for _, s := range specs {
		plan := compiled(t, s)
		covered[plan.Method()] = true

		p, err := New(plan, "row#1")
		if plan.Method() == probe.MethodNexthop && runtime.GOOS != "linux" {
			// Forcing is Linux's alone: elsewhere the target is refused at build.
			if !errors.Is(err, errNoTransport) {
				t.Errorf("nexthop built off Linux: %v", err)
			}

			continue
		}

		if err != nil || p == nil {
			t.Errorf("%s plan did not build: %v", plan.Method(), err)
		}

		if closer, ok := p.(interface{ Close() }); ok {
			closer.Close()
		}
	}

	for _, m := range probe.Methods() {
		if !covered[m] {
			t.Errorf("no sample plan for %s", m)
		}
	}
}

// An uncompiled zero plan cannot be dispatched.
func TestNewRejectsZeroPlan(t *testing.T) {
	_, err := New(probe.Plan{}, "row#1")
	if err == nil {
		t.Fatal("the tcp builder accepted a direct plan")
	}
}
