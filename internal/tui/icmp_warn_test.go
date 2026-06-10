package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/yuu61/deadman/internal/config"
)

// remedyColumn returns the display column where sub first starts among lines, or -1
// if absent. It mirrors view.go's go-runewidth measurement so the assertion reflects
// the column the renderer actually clips at.
func remedyColumn(lines []string, sub string) int {
	for _, l := range lines {
		if before, _, found := strings.Cut(l, sub); found {
			return runewidth.StringWidth(before)
		}
	}

	return -1
}

// TestMain pins the ICMP-availability probe to "available" so the rest of the tui
// tests stay deterministic regardless of whether the test host has CAP_NET_RAW or a
// configured ping_group_range. The missing-privilege path is covered explicitly by
// TestICMPPrivilegeWarningsEndToEnd, which flips the probe itself.
func TestMain(m *testing.M) {
	directICMPProbe = func() bool { return true }

	m.Run()
}

func TestNeedsLocalICMP(t *testing.T) {
	tests := []struct {
		name string
		spec config.TargetSpec
		want bool
	}{
		{"direct", config.TargetSpec{Name: "a", Addr: "8.8.8.8"}, true},
		{
			"nexthop",
			config.TargetSpec{
				Name:  "a",
				Addr:  "8.8.8.8",
				Relay: map[string]string{"nexthop": "192.0.2.1"},
			},
			true,
		},
		{
			"ssh relay",
			config.TargetSpec{
				Name:  "a",
				Addr:  "8.8.8.8",
				Relay: map[string]string{"relay": "host"},
			},
			false,
		},
		{
			"snmp",
			config.TargetSpec{Name: "a", Addr: "8.8.8.8", Relay: map[string]string{"via": "snmp"}},
			false,
		},
		{
			"netns",
			config.TargetSpec{Name: "a", Addr: "8.8.8.8", Relay: map[string]string{"via": "netns"}},
			false,
		},
		{"tcp", config.TargetSpec{Name: "a", Addr: "8.8.8.8", TCP: "dstport:80"}, false},
		{"separator", config.TargetSpec{IsSeparator: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsLocalICMP([]config.TargetSpec{tt.spec}); got != tt.want {
				t.Errorf("needsLocalICMP(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestNeedsLocalICMPRelayOnlyConfig(t *testing.T) {
	// A config mixing a separator with relay-only targets must not need local ICMP.
	specs := []config.TargetSpec{
		{IsSeparator: true},
		{Name: "s", Addr: "8.8.8.8", Relay: map[string]string{"relay": "host"}},
		{Name: "t", Addr: "8.8.8.8", TCP: "dstport:80"},
	}
	if needsLocalICMP(specs) {
		t.Error("needsLocalICMP = true for a relay-only config; want false (no local ICMP needed)")
	}
}

// TestICMPPrivilegeWarnings drives the warning end to end by pinning the
// availability probe, covering the spec-gating and the probe together: warn only
// when a direct/next-hop target is present AND no native path is available.
func TestICMPPrivilegeWarnings(t *testing.T) {
	orig := directICMPProbe

	t.Cleanup(func() { directICMPProbe = orig })

	direct := []config.TargetSpec{{Name: "a", Addr: "8.8.8.8"}}
	relayOnly := []config.TargetSpec{
		{Name: "s", Addr: "8.8.8.8", Relay: map[string]string{"relay": "h"}},
	}

	directICMPProbe = func() bool { return false }

	got := icmpPrivilegeWarnings(direct)
	if len(got) == 0 {
		t.Fatal("unprivileged host with a direct target should warn; got none")
	}

	// The setcap remedy must sit near the start of its line so the renderer (which
	// clips each warning to the terminal width, not wraps) keeps it visible even on
	// an 80-column terminal — view.go also prepends a 2-column "! " marker.
	const remedy = "setcap cap_net_raw+ep"

	col := remedyColumn(got, remedy)
	if col < 0 {
		t.Fatalf("no warning names the setcap remedy; got %v", got)
	}

	if col+2 >= 80 {
		t.Errorf(
			"setcap remedy starts at column %d (+2 marker); clipped on an 80-col terminal",
			col,
		)
	}

	if w := icmpPrivilegeWarnings(relayOnly); len(w) != 0 {
		t.Errorf("relay-only config should stay silent even unprivileged; got %v", w)
	}

	directICMPProbe = func() bool { return true }

	if w := icmpPrivilegeWarnings(direct); len(w) != 0 {
		t.Errorf("privileged host should stay silent; got %v", w)
	}
}

// TestICMPPrivilegeWarningNexthopRemedy verifies the next-hop remedy steers to
// CAP_NET_RAW only. A next-hop sends via AF_PACKET, so widening ping_group_range
// would not fix it yet would flip the probe to available and silence this warning —
// so the sysctl command must not be offered when a next-hop target is present.
func TestICMPPrivilegeWarningNexthopRemedy(t *testing.T) {
	orig := directICMPProbe

	t.Cleanup(func() { directICMPProbe = orig })

	directICMPProbe = func() bool { return false }

	nexthop := []config.TargetSpec{
		{Name: "gw", Addr: "8.8.8.8", Relay: map[string]string{"nexthop": "192.0.2.1"}},
	}

	got := icmpPrivilegeWarnings(nexthop)
	if len(got) == 0 {
		t.Fatal("unprivileged host with a next-hop target should warn; got none")
	}

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "setcap cap_net_raw+ep") {
		t.Errorf("next-hop remedy must offer setcap; got %q", joined)
	}

	if strings.Contains(joined, "sysctl") {
		t.Errorf(
			"next-hop remedy must not offer the ping_group_range sysctl "+
				"(it would silence the warning without fixing next-hop); got %q",
			joined,
		)
	}

	// A mixed direct + next-hop config must also suppress sysctl: setcap fixes both,
	// and offering sysctl would clear the warning while next-hop probes stay X.
	mixed := []config.TargetSpec{
		{Name: "d", Addr: "8.8.8.8"},
		{Name: "gw", Addr: "1.1.1.1", Relay: map[string]string{"nexthop": "192.0.2.1"}},
	}
	if strings.Contains(strings.Join(icmpPrivilegeWarnings(mixed), "\n"), "sysctl") {
		t.Error("mixed direct+next-hop config must not offer the ping_group_range sysctl")
	}
}

// TestICMPPrivilegeWarningRenders confirms the warning reaches the screen: an
// unprivileged host with a direct target surfaces the (always-visible, front-of-line)
// problem statement in the header, not just in the warning slice. The remedy's
// on-screen visibility is guarded separately by TestICMPPrivilegeWarnings.
func TestICMPPrivilegeWarningRenders(t *testing.T) {
	orig := directICMPProbe

	t.Cleanup(func() { directICMPProbe = orig })

	directICMPProbe = func() bool { return false }

	m, err := New([]config.TargetSpec{{Name: "a", Addr: "8.8.8.8"}}, Options{Scale: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "native ICMP unavailable") {
		t.Errorf("view should surface the ICMP-privilege warning\n---\n%s", out)
	}
}
