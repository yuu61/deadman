//go:build manual && linux

// These tests send real packets via AF_PACKET and need root (CAP_NET_RAW +
// CAP_NET_ADMIN to build the netns/veth topology). They are excluded from the
// default suite. Run them explicitly:
//
//	sudo go test -tags manual -run TestNexthop -v ./internal/ping
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
package ping

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestNDPLookupMatchesKernel validates the netlink RTM_GETNEIGH parsing (the
// riskiest, kernel-facing part of the IPv6 path: NdMsg field offsets, the NUD
// state mask, and the hand-rolled rtattr walk) against the kernel's own neighbor
// table. It needs no root — an RTM_GETNEIGH dump is unprivileged — so it runs
// without the netns setup; it skips when there is no usable IPv6 neighbor to
// check against.
func TestNDPLookupMatchesKernel(t *testing.T) {
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("iproute2 'ip' not found")
	}

	out, err := exec.Command("ip", "-6", "neigh", "show").CombinedOutput()
	if err != nil {
		t.Fatalf("ip -6 neigh show: %v\n%s", err, out)
	}

	ip, dev, mac := firstUsableV6Neighbor(string(out))
	if ip == nil {
		t.Skip("no usable IPv6 neighbor in the kernel cache to validate against")
	}

	ifi, err := net.InterfaceByName(dev)
	if err != nil {
		t.Fatalf("InterfaceByName(%q): %v", dev, err)
	}

	got, st := ndpLookup(ifi.Index, ip)
	if st == ndpMissing {
		t.Fatalf("ndpLookup(%d, %s) found nothing; kernel has lladdr %s", ifi.Index, ip, mac)
	}

	if got.String() != mac.String() {
		t.Fatalf("ndpLookup MAC = %s, kernel = %s", got, mac)
	}

	// A REACHABLE/PERMANENT kernel entry must be reported ndpReachable, not ndpStale.
	if state := lastField(string(out), ip.String()); state == "REACHABLE" || state == "PERMANENT" {
		if st != ndpReachable {
			t.Errorf("ndpLookup state = %d for a %s neighbor; want ndpReachable", st, state)
		}
	}

	t.Logf("ndpLookup(%s on %s) = %s state=%d — matches the kernel neighbor table",
		ip, dev, got, st)
}

// lastField returns the final whitespace-separated token (the NUD state) of the
// `ip -6 neigh show` line whose first field is addr, or "" if not found.
func lastField(out, addr string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == addr {
			return f[len(f)-1]
		}
	}

	return ""
}

// firstUsableV6Neighbor returns the first IPv6 neighbor from `ip -6 neigh show`
// output that carries an lladdr and is in a NUD state ndpLookup treats as usable.
func firstUsableV6Neighbor(out string) (net.IP, string, net.HardwareAddr) {
	usable := map[string]bool{
		"REACHABLE": true, "STALE": true, "DELAY": true, "PROBE": true, "PERMANENT": true,
	}

	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}

		ip := net.ParseIP(f[0])
		if ip == nil || ip.To4() != nil {
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
				mac, _ = net.ParseMAC(f[i+1])
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

	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("iproute2 'ip' not found")
	}

	setupNexthopTopology(t)

	p, err := newNexthopPinger(Spec{
		Addr:   nhPeerIP,
		Source: nhVeth, // egress interface; gateway is on-link.
		Relay:  map[string]string{"nexthop": nhPeerIP},
	})
	if err != nil {
		t.Fatalf("newNexthopPinger: %v", err)
	}

	res := p.Send(context.Background())
	if !res.Success {
		t.Fatalf("forced probe failed: %+v", res)
	}

	t.Logf("forced probe to %s via %s: rtt=%.3fms ttl=%d", nhPeerIP, nhPeerIP, res.RTT, res.TTL)
}

// TestNexthopForcedPipelineV6 is the IPv6 counterpart: it forces an ICMPv6 echo to
// the peer's global address, exercising the netlink NDP resolution, the AF_PACKET
// IPv6 send, and the raw ICMPv6 reply read. The gateway here is the peer's own
// global address (on-link on the veth), reached out the egress interface.
func TestNexthopForcedPipelineV6(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("iproute2 'ip' not found")
	}

	setupNexthopTopology(t)

	p, err := newNexthopPinger(Spec{
		Addr:   nhPeerIP6,
		Source: nhVeth, // egress interface; gateway is on-link.
		Relay:  map[string]string{"nexthop": nhPeerIP6},
	})
	if err != nil {
		t.Fatalf("newNexthopPinger: %v", err)
	}

	res := p.Send(context.Background())
	if !res.Success {
		t.Fatalf("forced IPv6 probe failed: %+v", res)
	}

	t.Logf(
		"forced IPv6 probe to %s via %s: rtt=%.3fms hoplimit=%d",
		nhPeerIP6, nhPeerIP6, res.RTT, res.TTL,
	)
}

// setupNexthopTopology creates a veth pair with the peer end in a child netns and
// registers cleanup. The host end stays in the test's (host) netns, so no setns
// dance is needed.
func setupNexthopTopology(t *testing.T) {
	t.Helper()

	// Best-effort teardown of any leftovers from a crashed run.
	_ = exec.Command("ip", "link", "del", nhVeth).Run()
	_ = exec.Command("ip", "netns", "del", nhNetns).Run()

	run(t, "ip", "netns", "add", nhNetns)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", nhNetns).Run() })

	run(t, "ip", "link", "add", nhVeth, "type", "veth", "peer", "name", nhVethP)
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", nhVeth).Run() })

	run(t, "ip", "link", "set", nhVethP, "netns", nhNetns)

	run(t, "ip", "addr", "add", nhHostIP+nhPrefix, "dev", nhVeth)
	run(t, "ip", "link", "set", nhVeth, "up")

	run(t, "ip", "netns", "exec", nhNetns, "ip", "addr", "add", nhPeerIP+nhPrefix, "dev", nhVethP)
	run(t, "ip", "netns", "exec", nhNetns, "ip", "link", "set", nhVethP, "up")
	run(t, "ip", "netns", "exec", nhNetns, "ip", "link", "set", "lo", "up")

	// Add the IPv6 addresses with nodad so the global address is usable immediately
	// (duplicate-address detection would otherwise keep it tentative for ~1s).
	run(t, "ip", "-6", "addr", "add", nhHostIP6+nhPrefix6, "dev", nhVeth, "nodad")
	run(t, "ip", "netns", "exec", nhNetns,
		"ip", "-6", "addr", "add", nhPeerIP6+nhPrefix6, "dev", nhVethP, "nodad")
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()

	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
