//go:build manual && linux

// These tests send real packets via AF_PACKET and need root (CAP_NET_RAW +
// CAP_NET_ADMIN to build the netns/veth topology). They are excluded from the
// default suite. Run them explicitly:
//
//	sudo go test -tags manual -run TestNexthop -v ./internal/infrastructure/prober
//
// TestNexthopForcedPipeline builds an isolated veth link with the peer in a child
// network namespace and forces an ICMP probe out to it, exercising the whole
// pipeline: egress selection, ARP resolution, AF_PACKET L2 send to the gateway
// MAC, and the raw-ICMP reply read.
//
// Proving that forcing *overrides* the routing table (the real point of the
// feature) needs a two-path topology and a packet capture, which cannot be
// asserted from inside one process. Do it by hand:
//
//	ns-host(A) ── ns-r1(R1) ── ns-target(T)   # A's default route
//	          └── ns-r2(R2) ──┘                # forced next-hop
//
//	# in A, with R1 as the default route:
//	sudo ip netns exec ns-host bin/deadman conf-with "t T nexthop=<R2-near-addr>"
//	# capture on R2 and confirm the echo traverses it (L2 dst = R2's MAC):
//	sudo ip netns exec ns-r2 tcpdump -e -ni <veth> icmp
//
// Flip net.ipv4.conf.*.rp_filter between 1/2/0 on A to observe the strict-mode
// silent drop (and deadman's startup warning) versus loose/off succeeding.

package prober

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// TestNeighLookupMatchesKernel validates the netlink RTM_GETNEIGH parsing (the
// riskiest, kernel-facing part of gateway resolution: NdMsg field offsets, the NUD
// state mask, and the hand-rolled rtattr walk) against the kernel's own neighbor
// table, for ARP and for NDP. It needs no root — an RTM_GETNEIGH dump is unprivileged —
// so it runs without the netns setup; a family skips when there is no usable neighbor
// of it to check against.
func TestNeighLookupMatchesKernel(t *testing.T) {
	requireIP(t)

	for _, fam := range []string{"-4", "-6"} {
		t.Run(fam, func(t *testing.T) {
			out, err := exec.CommandContext(t.Context(), "ip", fam, "neigh", "show").
				CombinedOutput()
			if err != nil {
				t.Fatalf("ip %s neigh show: %v\n%s", fam, err, out)
			}

			ip, dev, mac := firstUsableNeighbor(string(out))
			if ip == nil {
				t.Skip("no usable neighbor of this family in the kernel cache")
			}

			ifi, err := net.InterfaceByName(dev)
			if err != nil {
				t.Fatalf("InterfaceByName(%q): %v", dev, err)
			}

			got, st, err := neighLookup(t.Context(), ifi.Index, ip)
			if err != nil {
				t.Fatal(err)
			}

			if st == neighMissing {
				t.Fatalf(
					"neighLookup(%d, %s) found nothing; kernel has lladdr %s",
					ifi.Index,
					ip,
					mac,
				)
			}

			if got.String() != mac.String() {
				t.Fatalf("neighLookup MAC = %s, kernel = %s", got, mac)
			}

			// A REACHABLE/PERMANENT kernel entry must be reported neighReachable, and any
			// other usable one neighStale.
			state := lastField(string(out), ip.String())

			want := state == "REACHABLE" || state == "PERMANENT"
			if (st == neighReachable) != want {
				t.Errorf("neighLookup state = %d for a %s neighbor", st, state)
			}

			t.Logf("neighLookup(%s on %s) = %s state=%d — matches the kernel neighbor table",
				ip, dev, got, st)
		})
	}
}

// lastField returns the final whitespace-separated token (the NUD state) of the
// `ip neigh show` line whose first field is addr, or "" if not found.
func lastField(out, addr string) string {
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == addr {
			return f[len(f)-1]
		}
	}

	return ""
}

// TestKernelPreferredSrcMatchesRouteGet validates that kernelPreferredSrc (the
// ping6-style connect/getsockname source selection) agrees with the kernel's own
// `ip -6 route get`. It is root-free (a UDP connect is unprivileged) and skips on a
// host without global IPv6 connectivity.
func TestKernelPreferredSrcMatchesRouteGet(t *testing.T) {
	requireIP(t)

	const dst = "2001:4860:4860::8888" // a public global IPv6, used only as a route key.

	out, err := exec.CommandContext(t.Context(), "ip", "-6", "route", "get", dst).CombinedOutput()
	if err != nil {
		t.Skipf("no IPv6 route to %s: %v", dst, err)
	}

	want := routeGetSrc(string(out))
	if want == nil {
		t.Skip("`ip -6 route get` reported no source (no global IPv6 on this host)")
	}

	got := kernelPreferredSrc(t.Context(), net.ParseIP(dst))
	if got == nil {
		t.Fatalf("kernelPreferredSrc(%s) = nil; `ip route get` src = %s", dst, want)
	}

	if !got.Equal(want) {
		t.Fatalf("kernelPreferredSrc = %s, `ip -6 route get` src = %s", got, want)
	}

	t.Logf("kernelPreferredSrc(%s) = %s — matches `ip -6 route get`", dst, got)
}

// routeGetSrc extracts the "src <addr>" field from `ip -6 route get` output.
func routeGetSrc(out string) net.IP {
	f := strings.Fields(out)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "src" {
			return net.ParseIP(f[i+1])
		}
	}

	return nil
}

// firstUsableNeighbor returns the first neighbor from `ip neigh show` output that
// carries an lladdr and is in a NUD state neighLookup treats as usable.
func firstUsableNeighbor(out string) (net.IP, string, net.HardwareAddr) {
	usable := map[string]bool{
		"REACHABLE": true, "STALE": true, "DELAY": true, "PROBE": true, "PERMANENT": true,
	}

	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}

		ip := net.ParseIP(f[0])
		if ip == nil {
			continue
		}

		var (
			dev string
			mac net.HardwareAddr
		)

		for i := 1; i+1 < len(f); i++ {
			switch f[i] {
			case "dev":
				dev = f[i+1]
			case "lladdr":
				parsed, err := net.ParseMAC(f[i+1])
				if err == nil {
					mac = parsed
				}
			default:
				// The other tokens (proxy, router, the NUD state) are not needed here.
			}
		}

		if dev != "" && mac != nil && usable[f[len(f)-1]] {
			return ip, dev, mac
		}
	}

	return nil, "", nil
}

const (
	nhNetns   = "dmnh"
	nhVeth    = "dmh0"
	nhVethP   = "dmh0p"
	nhHostIP  = "10.123.45.1"
	nhPeerIP  = "10.123.45.2"
	nhPrefix  = "/24"
	nhHostIP6 = "2001:db8:dead::1"
	nhPeerIP6 = "2001:db8:dead::2"
	nhPrefix6 = "/64"
)

func TestNexthopForcedPipeline(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	requireIP(t)

	setupNexthopTopology(t)

	p, err := New(compiled(t, probe.Spec{
		Addr: nhPeerIP,

		Params: probe.Nexthop{
			Source:  probe.SourceInterface(nhVeth),
			Gateway: netip.MustParseAddr(nhPeerIP),
		},
	}), "row#1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res := p.Send(t.Context())
	if !res.IsSuccess() {
		t.Fatalf("forced probe failed: %+v", res)
	}

	t.Logf("forced probe to %s via %s: rtt=%.3fms", nhPeerIP, nhPeerIP, res.RTT)
}

// TestNexthopForcedPipelineV6 is the IPv6 counterpart: it forces an ICMPv6 echo to
// the peer's global address, exercising the netlink NDP resolution, the AF_PACKET
// IPv6 send, and the raw ICMPv6 reply read. The gateway here is the peer's own
// global address (on-link on the veth), reached out the egress interface.
func TestNexthopForcedPipelineV6(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	requireIP(t)

	setupNexthopTopology(t)

	p, err := New(compiled(t, probe.Spec{
		Addr: nhPeerIP6,

		Params: probe.Nexthop{
			Source:  probe.SourceInterface(nhVeth),
			Gateway: netip.MustParseAddr(nhPeerIP6),
		},
	}), "row#1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res := p.Send(t.Context())
	if !res.IsSuccess() {
		t.Fatalf("forced IPv6 probe failed: %+v", res)
	}

	t.Logf("forced IPv6 probe to %s via %s: rtt=%.3fms", nhPeerIP6, nhPeerIP6, res.RTT)
}

// TestNexthopRecoversAChangedGatewayMAC leaves the kernel an outdated, merely STALE
// neighbor entry for the gateway, as a replaced gateway without gratuitous ARP does. A
// forced send bypasses the neighbor subsystem, so only a revalidation the prober asks
// for can correct it: the probe must recover within a few rounds instead of reading X
// for as long as the stale entry lives.
func TestNexthopRecoversAChangedGatewayMAC(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	requireIP(t)

	setupNexthopTopology(t)
	runIP(t, "neigh", "replace", nhPeerIP, "lladdr", "02:00:00:00:00:99",
		"dev", nhVeth, "nud", "stale")

	p, err := New(compiled(t, probe.Spec{
		Addr: nhPeerIP,

		Params: probe.Nexthop{
			Source:  probe.SourceInterface(nhVeth),
			Gateway: netip.MustParseAddr(nhPeerIP),
		},
	}), "row#1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	for round := 1; time.Since(start) < 30*time.Second; round++ {
		if res := p.Send(t.Context()); res.IsSuccess() {
			t.Logf("recovered in round %d after %v", round, time.Since(start).Round(time.Second))

			return
		}

		pause(t, time.Second)
	}

	t.Fatal("forced probe never recovered from the outdated gateway MAC")
}

// setupNexthopTopology creates a veth pair with the peer end in a child netns and
// registers cleanup. The host end stays in the test's (host) netns, so no setns
// dance is needed.
func setupNexthopTopology(t *testing.T) {
	t.Helper()

	// Best-effort teardown of any leftovers from a crashed run.
	tryIP(t, "link", "del", nhVeth)
	tryIP(t, "netns", "del", nhNetns)

	runIP(t, "netns", "add", nhNetns)
	t.Cleanup(func() { tryIP(t, "netns", "del", nhNetns) })

	runIP(t, "link", "add", nhVeth, "type", "veth", "peer", "name", nhVethP)
	t.Cleanup(func() { tryIP(t, "link", "del", nhVeth) })

	runIP(t, "link", "set", nhVethP, "netns", nhNetns)

	runIP(t, "addr", "add", nhHostIP+nhPrefix, "dev", nhVeth)
	runIP(t, "link", "set", nhVeth, "up")

	runIP(t, "netns", "exec", nhNetns, "ip", "addr", "add", nhPeerIP+nhPrefix, "dev", nhVethP)
	runIP(t, "netns", "exec", nhNetns, "ip", "link", "set", nhVethP, "up")
	runIP(t, "netns", "exec", nhNetns, "ip", "link", "set", "lo", "up")

	// Add the IPv6 addresses with nodad so the global address is usable immediately
	// (duplicate-address detection would otherwise keep it tentative for ~1s).
	runIP(t, "-6", "addr", "add", nhHostIP6+nhPrefix6, "dev", nhVeth, "nodad")
	runIP(t, "netns", "exec", nhNetns,
		"ip", "-6", "addr", "add", nhPeerIP6+nhPrefix6, "dev", nhVethP, "nodad")
}

// requireIP skips the test unless iproute2's ip is installed.
func requireIP(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("iproute2 'ip' not found")
	}
}

// runIP runs iproute2's ip with args and fails the test if it does.
func runIP(t *testing.T, args ...string) {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %v: %v\n%s", args, err, out)
	}
}

// tryIP runs ip with args for a teardown that may find nothing to remove, and logs
// instead of failing. It outlives the test's context, which is canceled before cleanup.
func tryIP(t *testing.T, args ...string) {
	t.Helper()

	out, err := exec.CommandContext(context.WithoutCancel(t.Context()), "ip", args...).
		CombinedOutput()
	if err != nil {
		t.Logf("ip %v (ignored): %v\n%s", args, err, out)
	}
}
