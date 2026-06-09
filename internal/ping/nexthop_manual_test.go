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
	"os"
	"os/exec"
	"testing"
)

const (
	nhNetns  = "dmnh"
	nhVeth   = "dmh0"
	nhVethP  = "dmh0p"
	nhHostIP = "10.123.45.1"
	nhPeerIP = "10.123.45.2"
	nhPrefix = "/24"
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
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()

	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
