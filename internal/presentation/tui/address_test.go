package tui

import (
	"net/netip"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestViewDistinguishesResolveFamily(t *testing.T) {
	for _, family := range []probe.Family{probe.FamilyIPv4, probe.FamilyIPv6} {
		methods := []probe.Params{
			probe.Direct{Family: family},
			probe.QUIC{Family: family},
			probe.TCP{Port: probe.PortNumber(80), Family: family},
			probe.SSH{Host: "jump", OS: probe.OSLinux, Family: family},
			probe.Netns{Name: "ns", Family: family},
			probe.VRF{Name: "vrf", Family: family},
		}
		for _, params := range methods {
			spec := config.Target{Name: "google", Addr: "google.com", Params: params}
			m := newModel(
				t,
				[]config.Line{spec},
				testOptions{Columns: map[string]bool{"VIA": false}},
			)
			_, out := drive(t, m, tea.WindowSizeMsg{Width: 180, Height: 20})

			want := "google.com [" + family.String() + "]"
			if !strings.Contains(out, want) {
				t.Errorf("%T: view missing %q\n%s", params, want, out)
			}
		}
	}
}

func TestViewAddressFamilyConditions(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		params probe.Params
		want   string
	}{
		{"unconstrained", "google.com", probe.Direct{}, "google.com"},
		{"unconstrained_quic", "google.com", probe.QUIC{}, "google.com"},
		{
			"interface",
			"google.com",
			probe.Direct{Source: probe.SourceInterface("eth0")},
			"google.com",
		},
		{
			"source_ipv4", "google.com",
			probe.Direct{Source: probe.SourceAddr(netip.MustParseAddr("192.0.2.1"))},
			"google.com [IPv4]",
		},
		{
			"source_ipv6",
			"google.com",
			probe.SSH{
				Host:   "jump",
				OS:     probe.OSLinux,
				Source: probe.SourceAddr(netip.MustParseAddr("2001:db8::1")),
			},
			"google.com [IPv6]",
		},
		{"literal_ipv4", "192.0.2.1", probe.Direct{Family: probe.FamilyIPv4}, "192.0.2.1"},
		{"literal_ipv6", "2001:db8::1", probe.Direct{Family: probe.FamilyIPv6}, "2001:db8::1"},
		{"rejected", "192.0.2.1", probe.Direct{Family: probe.FamilyIPv6}, "192.0.2.1"},
		{"tcp", "google.com", probe.TCP{Port: probe.PortNumber(80)}, "google.com [IPv4]"},
		{"snmp", "google.com", probe.SNMP{Host: "agent", Community: "public"}, "google.com"},
		{
			"routeros",
			"google.com",
			probe.RouterOS{Host: "router", Username: "user", Password: "pass"},
			"google.com",
		},
		{
			"nexthop",
			"google.com",
			probe.Nexthop{Gateway: netip.MustParseAddr("192.0.2.1")},
			"google.com",
		},
		{
			"long_hostname", strings.Repeat("a", 50) + ".example",
			probe.Direct{Family: probe.FamilyIPv6},
			strings.Repeat("a", 33) + " [IPv6]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newModel(
				t,
				[]config.Line{config.Target{Name: "host", Addr: c.addr, Params: c.params}},
				testOptions{},
			)
			m, out := drive(t, m, tea.WindowSizeMsg{Width: 180, Height: 20})

			line := m.rowLine(0, m.width)
			if !strings.Contains(line, c.want) ||
				(!strings.Contains(c.want, " [IPv") && strings.Contains(line, " [IPv")) {
				t.Errorf("row address should be %q\n%s", c.want, line)
			}

			assertNoLineExceedsWidth(t, out, 180)

			if !isRejected(m.rows()[0]) && lipgloss.Width(line) != m.rowFixedWidth() {
				t.Errorf(
					"row width = %d, fixed width = %d",
					lipgloss.Width(line),
					m.rowFixedWidth(),
				)
			}

			if got := snapshotAtAddress(t, m); got != c.addr {
				t.Errorf("stored address = %q, want %q", got, c.addr)
			}
		})
	}
}

func snapshotAtAddress(t *testing.T, m Model) string {
	t.Helper()

	if isRejected(m.rows()[0]) {
		return rowIdentity(m.rows()[0]).addr
	}

	return snapshotAt(t, m, 0).Addr
}

func TestViewCanonicalIPAddresses(t *testing.T) {
	for raw, want := range map[string]string{
		"008.008.008.008":             "8.8.8.8",
		"2001:4860:4860:0:0:0:0:8888": "2001:4860:4860::8888",
		"2001:4860:4860:0:0::8888":    "2001:4860:4860::8888",
		"FE80:0:0:0:0:0:0:1%eth0":     "fe80::1%eth0",
		"::ffff:192.000.002.001":      "192.0.2.1",
	} {
		m := newModel(t, []config.Line{config.Target{Name: "host", Addr: raw}}, testOptions{})

		m, out := drive(t, m, tea.WindowSizeMsg{Width: 180, Height: 20})
		if !strings.Contains(out, want) || strings.Contains(out, raw) {
			t.Errorf("%s: view should show %s\n%s", raw, want, out)
		}

		if got := snapshotAtAddress(t, m); got != want {
			t.Errorf("stored address = %q, want %q", got, want)
		}
	}
}

func TestViewAddressFamilyReload(t *testing.T) {
	spec := config.Target{
		Name:   "google",
		Addr:   "google.com",
		Params: probe.Direct{Family: probe.FamilyIPv4},
	}
	src := sourceOf([]config.Line{spec}, testOptions{})
	m := openModel(t, testService(stubHost{}, src), testOptions{})

	m, before := drive(t, m, tea.WindowSizeMsg{Width: 180, Height: 20})
	if !strings.Contains(before, "google.com [IPv4]") {
		t.Fatalf("initial view missing IPv4\n%s", before)
	}

	spec.Params = probe.Direct{Family: probe.FamilyIPv6}
	src.cfg.Lines = []config.Line{spec}

	_, after := drive(t, m, reloadMsg{})
	if !strings.Contains(after, "google.com [IPv6]") || strings.Contains(after, "[IPv4]") {
		t.Fatalf("reloaded view did not switch family\n%s", after)
	}
}
