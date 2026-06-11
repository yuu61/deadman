//go:build linux

package ping

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/net/icmp"
	netipv4 "golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// echoReplyV4 marshals an ICMPv4 echo reply as it appears on the wire without an IP
// header — what a datagram socket delivers and what payload yields after stripping.
func echoReplyV4(t *testing.T, id, seq int, token []byte) []byte {
	t.Helper()

	msg := icmp.Message{
		Type: netipv4.ICMPTypeEchoReply,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: token},
	}

	b, err := msg.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// v4Probe builds an icmpProbe for an IPv4 target (seq 1) with the given privilege and id.
func v4Probe(privileged bool, id int, token []byte) icmpProbe {
	return icmpProbe{
		fam:        familyOf(net.IPv4(127, 0, 0, 1)),
		privileged: privileged,
		id:         id,
		seq:        1,
		token:      token,
	}
}

// TestProbePayloadStripsRawV4Header pins the one path that strips a leading IP header:
// the privileged raw IPv4 socket. Datagram (v4/v6) and raw v6 deliver the ICMP message
// directly. Mis-stripping here would drop every reply to an X.
func TestProbePayloadStripsRawV4Header(t *testing.T) {
	icmpBytes := echoReplyV4(t, 1, 1, []byte("tok"))

	// A 20-byte IPv4 header: version 4, IHL 5 (×4 = 20 bytes).
	rawV4 := append(
		[]byte{0x45, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		icmpBytes...)

	got, ok := v4Probe(true, 1, nil).payload(rawV4)
	if !ok || !bytes.Equal(got, icmpBytes) {
		t.Fatalf("raw v4 strip: ok=%v got=%v want=%v", ok, got, icmpBytes)
	}

	// Datagram v4 passes the bytes through untouched.
	if got, ok := v4Probe(false, 1, nil).payload(icmpBytes); !ok || !bytes.Equal(got, icmpBytes) {
		t.Fatalf("datagram v4 passthrough: ok=%v", ok)
	}

	// Raw v6 also passes through (only raw v4 carries an IP header).
	v6 := icmpProbe{fam: familyOf(net.ParseIP("::1")), privileged: true}
	if got, ok := v6.payload(icmpBytes); !ok || !bytes.Equal(got, icmpBytes) {
		t.Fatalf("raw v6 passthrough: ok=%v", ok)
	}

	// A header claiming more length than present must be rejected, not panic.
	if _, ok := v4Probe(true, 1, nil).payload([]byte{0x4f}); ok {
		t.Fatal("truncated raw v4 header should be rejected")
	}
}

// TestProbeMatchIDOnlyOnRawPath pins the raw-vs-datagram id rule: the kernel rewrites our
// id on the datagram path (so id must be ignored there), while the raw socket sees all
// ICMP and needs the id to disambiguate.
func TestProbeMatchIDOnlyOnRawPath(t *testing.T) {
	const ourID, seq = 42, 1

	token := []byte("deadman1")
	peer := net.ParseIP("127.0.0.1")
	from := &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}

	// Datagram reply carries the kernel-assigned id (not ours): must still match.
	dgram := echoReplyV4(t, 9999, seq, token)
	if !v4Probe(false, ourID, token).match(dgram, from, peer) {
		t.Fatal("datagram path must match despite a rewritten id")
	}

	// Raw reply with our id matches; with a foreign id it must not.
	if !v4Probe(true, ourID, token).match(echoReplyV4(t, ourID, seq, token), from, peer) {
		t.Fatal("raw path must match its own id")
	}

	if v4Probe(true, ourID, token).match(echoReplyV4(t, 9999, seq, token), from, peer) {
		t.Fatal("raw path must reject a foreign id")
	}
}

// TestProbeMatchRejectsImposters pins seq/token/source filtering — the guard against a
// concurrent prober's reply (raw fan-out) being folded into the wrong target's stats.
func TestProbeMatchRejectsImposters(t *testing.T) {
	const id, seq = 7, 3

	token := []byte("realtok0")
	peer := net.ParseIP("10.0.0.1")
	from := &unix.SockaddrInet4{Addr: [4]byte{10, 0, 0, 1}}

	pr := icmpProbe{fam: familyOf(peer), privileged: false, id: id, seq: seq, token: token}

	if !pr.match(echoReplyV4(t, id, seq, token), from, peer) {
		t.Fatal("the genuine reply must match")
	}

	if pr.match(echoReplyV4(t, id, seq, []byte("OTHERtok")), from, peer) {
		t.Fatal("a foreign token must be rejected")
	}

	if pr.match(echoReplyV4(t, id, seq+1, token), from, peer) {
		t.Fatal("a foreign seq must be rejected")
	}

	if pr.match(
		echoReplyV4(t, id, seq, token),
		&unix.SockaddrInet4{Addr: [4]byte{10, 0, 0, 2}},
		peer,
	) {
		t.Fatal("a reply from another source must be rejected")
	}

	// A missing source address must not drop an otherwise-valid reply.
	if !pr.match(echoReplyV4(t, id, seq, token), nil, peer) {
		t.Fatal("a nil source must not reject a matching reply")
	}
}

// TestTimespecToTimeValidation pins the SCM_TIMESTAMPNS guard: a zero or out-of-range
// stamp is rejected so the caller falls back to a userspace instant instead of computing
// an RTT against epoch.
func TestTimespecToTimeValidation(t *testing.T) {
	tsBytes := func(ts unix.Timespec) []byte {
		p := (*[64]byte)(unsafe.Pointer(&ts)) //nolint:gosec // read a fixed-layout struct as bytes.
		b := make([]byte, unsafe.Sizeof(ts))
		copy(b, p[:unsafe.Sizeof(ts)])

		return b
	}

	got, ok := timespecToTime(tsBytes(unix.Timespec{Sec: 100, Nsec: 500}))
	if !ok || !got.Equal(time.Unix(100, 500)) {
		t.Fatalf("valid timespec: ok=%v got=%v", ok, got)
	}

	if _, ok := timespecToTime(tsBytes(unix.Timespec{Sec: 0, Nsec: 0})); ok {
		t.Fatal("zero timespec must be rejected")
	}

	if _, ok := timespecToTime(tsBytes(unix.Timespec{Sec: 1, Nsec: int64(2 * time.Second)})); ok {
		t.Fatal("out-of-range nsec must be rejected")
	}

	if _, ok := timespecToTime([]byte{0x01, 0x02}); ok {
		t.Fatal("a short buffer must be rejected")
	}
}

// scmTimestampOOB packs a SCM_TIMESTAMPNS control message carrying when, as the kernel
// would deliver it, so result()'s clock-step handling can be tested without a socket.
func scmTimestampOOB(when time.Time) []byte {
	ts := unix.NsecToTimespec(when.UnixNano())
	sz := int(unsafe.Sizeof(ts))

	b := make([]byte, unix.CmsgSpace(sz))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&b[0])) //nolint:gosec // build a fixed-layout cmsg.
	h.Level = unix.SOL_SOCKET
	h.Type = unix.SCM_TIMESTAMPNS
	h.SetLen(unix.CmsgLen(sz))

	tsp := (*[64]byte)(unsafe.Pointer(&ts)) //nolint:gosec // view the timespec as bytes.
	copy(b[unix.CmsgLen(0):], tsp[:sz])

	return b
}

// TestResultClockStepFallback pins F5: the kernel timestamp is used only when plausible
// (in [0, monotonic span]); a backward or forward CLOCK_REALTIME step falls back to the
// step-immune monotonic span instead of yielding a negative/huge RTT.
func TestResultClockStepFallback(t *testing.T) {
	pr := v4Probe(false, 1, nil)

	tSend := time.Now()
	recvUser := tSend.Add(10 * time.Millisecond) // monotonic span = 10ms.

	cases := []struct {
		name   string
		oob    []byte
		wantMs float64
	}{
		{"plausible kernel ts", scmTimestampOOB(tSend.Add(3 * time.Millisecond)), 3.0},
		{"forward step (k>span)", scmTimestampOOB(tSend.Add(50 * time.Millisecond)), 10.0},
		{"backward step (k<0)", scmTimestampOOB(tSend.Add(-5 * time.Millisecond)), 10.0},
		{"no kernel ts", nil, 10.0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := pr.result(c.oob, recvUser, tSend)
			if !res.Success {
				t.Fatal("result must be a success")
			}

			if res.RTT < c.wantMs-0.5 || res.RTT > c.wantMs+0.5 {
				t.Fatalf("RTT=%.3fms, want ~%.1fms", res.RTT, c.wantMs)
			}
		})
	}
}

// TestZoneIndex pins F3's zone parsing: numeric scope ids and interface names both resolve,
// and an unknown zone degrades to 0 (no zone) rather than erroring.
func TestZoneIndex(t *testing.T) {
	if got := zoneIndex("2"); got != 2 {
		t.Errorf("numeric zone %q = %d, want 2", "2", got)
	}

	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip("no lo interface")
	}

	if got := zoneIndex("lo"); got != lo.Index {
		t.Errorf("named zone %q = %d, want %d", "lo", got, lo.Index)
	}

	if got := zoneIndex("nonexistent-if-zzz"); got != 0 {
		t.Errorf("unknown zone = %d, want 0", got)
	}
}

// TestLiteralAddrZone pins F3: a scoped IPv6 literal keeps its zone as an interface index;
// a plain address has zone 0; the resolve_family pin still rejects the wrong family.
func TestLiteralAddrZone(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip("no lo interface")
	}

	p := &icmpPinger{}

	ip, zone, err := p.literalAddr(netip.MustParseAddr("fe80::1%lo"))
	if err != nil || ip == nil || zone != lo.Index {
		t.Fatalf("fe80::1%%lo -> ip=%v zone=%d err=%v, want zone=%d", ip, zone, err, lo.Index)
	}

	_, zone, err = p.literalAddr(netip.MustParseAddr("192.0.2.1"))
	if err != nil || zone != 0 {
		t.Fatalf("192.0.2.1 -> zone=%d err=%v, want zone=0", zone, err)
	}

	pinned := &icmpPinger{network: networkIPv4}

	_, _, perr := pinned.literalAddr(netip.MustParseAddr("::1"))
	if perr == nil {
		t.Fatal("resolve_family=ipv4 must reject a v6 literal")
	}
}

// TestEgressControlNonEmpty pins F1: an interface source produces a non-empty IP_PKTINFO /
// IPV6_PKTINFO control message (the privilege-free egress selector that replaces
// SO_BINDTODEVICE).
func TestEgressControlNonEmpty(t *testing.T) {
	if oob := familyOf(net.IPv4(127, 0, 0, 1)).egressControl(1); len(oob) == 0 {
		t.Error("v4 egress control message is empty")
	}

	if oob := familyOf(net.ParseIP("::1")).egressControl(1); len(oob) == 0 {
		t.Error("v6 egress control message is empty")
	}
}
