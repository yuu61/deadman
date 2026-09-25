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

// pinProbes fixes both ICMP-availability probes for one test and restores them
// afterward, so a test can stage a precise privilege scenario (raw and/or datagram
// open) without depending on the runner's actual CAP_NET_RAW or ping_group_range.
func pinProbes(t *testing.T, raw, direct bool) {
	t.Helper()

	origRaw, origDirect := rawICMPProbe, directICMPProbe

	t.Cleanup(func() { rawICMPProbe, directICMPProbe = origRaw, origDirect })

	rawICMPProbe = func() bool { return raw }
	directICMPProbe = func() bool { return direct }
}

// TestMain pins both ICMP-availability probes to "available" so the rest of the tui
// tests stay deterministic regardless of the runner's CAP_NET_RAW or ping_group_range.
// The missing-privilege paths are covered explicitly by the TestICMPPrivilegeWarning*
// tests, which pin the probes themselves.
func TestMain(m *testing.M) {
	directICMPProbe = func() bool { return true }
	rawICMPProbe = func() bool { return true }

	m.Run()
}

func TestLocalICMPNeeds(t *testing.T) {
	tests := []struct {
		name        string
		spec        config.TargetSpec
		wantDirect  bool
		wantNexthop bool
	}{
		{"direct", config.TargetSpec{Name: "a", Addr: "8.8.8.8"}, true, false},
		{
			"nexthop",
			config.TargetSpec{
				Name:  "a",
				Addr:  "8.8.8.8",
				Relay: map[string]string{"nexthop": "192.0.2.1"},
			},
			false,
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
			false,
		},
		{
			"snmp",
			config.TargetSpec{Name: "a", Addr: "8.8.8.8", Relay: map[string]string{"via": "snmp"}},
			false, false,
		},
		{"tcp", config.TargetSpec{Name: "a", Addr: "8.8.8.8", TCP: "dstport:80"}, false, false},
		{"separator", config.TargetSpec{IsSeparator: true}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := localICMPNeeds([]config.TargetSpec{tt.spec})
			if got.direct != tt.wantDirect || got.nexthop != tt.wantNexthop {
				t.Errorf(
					"localICMPNeeds(%s) = (direct=%v, nexthop=%v), want (%v, %v)",
					tt.name, got.direct, got.nexthop, tt.wantDirect, tt.wantNexthop,
				)
			}
		})
	}
}

func TestLocalICMPNeedsRelayOnly(t *testing.T) {
	specs := []config.TargetSpec{
		{IsSeparator: true},
		{Name: "s", Addr: "8.8.8.8", Relay: map[string]string{"relay": "host"}},
		{Name: "t", Addr: "8.8.8.8", TCP: "dstport:80"},
	}
	if got := localICMPNeeds(specs); got.direct || got.nexthop {
		t.Errorf(
			"relay-only config: localICMPNeeds = (direct=%v, nexthop=%v), want (false, false)",
			got.direct, got.nexthop,
		)
	}
}

// TestICMPPrivilegeWarnings covers the direct-ICMP path: warn (with the sysctl
// remedy) when a direct target cannot open either native socket, and stay silent for
// relay-only configs or when a path is available.
func TestICMPPrivilegeWarnings(t *testing.T) {
	pinProbes(t, true, false) // raw value irrelevant here; direct path unavailable.

	direct := []config.TargetSpec{{Name: "a", Addr: "8.8.8.8"}}
	relayOnly := []config.TargetSpec{
		{Name: "s", Addr: "8.8.8.8", Relay: map[string]string{"relay": "h"}},
	}

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
		t.Errorf("available direct path should stay silent; got %v", w)
	}
}

// TestICMPPrivilegeWarningNexthopRemedy verifies the next-hop remedy steers to
// CAP_NET_RAW only. A next-hop sends via AF_PACKET, so widening ping_group_range
// would not fix it yet would flip the direct gate to available and silence this
// warning — so the sysctl command must not be offered when a next-hop is present.
func TestICMPPrivilegeWarningNexthopRemedy(t *testing.T) {
	pinProbes(t, false, false) // both native paths dead.

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

// TestICMPPrivilegeWarningNexthopRawOnly is the regression for the raw-vs-datagram
// gap: with the datagram path up but no CAP_NET_RAW, a direct target probes fine
// while a next-hop (AF_PACKET) cannot. The next-hop must still be warned about, and a
// direct-only config in the same environment must stay silent.
func TestICMPPrivilegeWarningNexthopRawOnly(t *testing.T) {
	pinProbes(t, false, true) // raw down, datagram up.

	nexthop := []config.TargetSpec{
		{Name: "gw", Addr: "8.8.8.8", Relay: map[string]string{"nexthop": "192.0.2.1"}},
	}

	got := icmpPrivilegeWarnings(nexthop)
	if len(got) == 0 {
		t.Fatal("next-hop with raw unavailable (datagram up) must still warn; got none")
	}

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "setcap cap_net_raw+ep") {
		t.Errorf("remedy must offer setcap; got %q", joined)
	}

	if strings.Contains(joined, "sysctl") {
		t.Errorf("next-hop remedy must not offer the ping_group_range sysctl; got %q", joined)
	}

	direct := []config.TargetSpec{{Name: "d", Addr: "8.8.8.8"}}
	if w := icmpPrivilegeWarnings(direct); len(w) != 0 {
		t.Errorf("direct-only config works over datagram and must stay silent; got %v", w)
	}
}

// TestICMPPrivilegeWarningRenders confirms the warning reaches the screen: an
// unprivileged host with a direct target surfaces the (always-visible, front-of-line)
// problem statement in the header, not just in the warning slice. The remedy's
// on-screen visibility is guarded separately by TestICMPPrivilegeWarnings.
func TestICMPPrivilegeWarningRenders(t *testing.T) {
	pinProbes(t, true, false)

	m := newModel(t, []config.TargetSpec{{Name: "a", Addr: "8.8.8.8"}}, Options{Scale: 10})

	_, out := drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "native ICMP unavailable") {
		t.Errorf("view should surface the ICMP-privilege warning\n---\n%s", out)
	}
}
