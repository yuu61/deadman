//go:build linux

package prober

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// echoReplyV4 marshals an ICMPv4 echo reply as it appears on the wire without an IP
// header — what a datagram socket delivers and what payload yields after stripping.
func echoReplyV4(t *testing.T, id, seq int, token []byte) []byte {
	t.Helper()

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
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
		icmpBytes...,
	)

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
		b, err := binary.Append(nil, binary.NativeEndian, ts)
		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	got, ok := timespecToTime(tsBytes(unix.Timespec{Sec: 100, Nsec: 500}))
	if !ok || !got.Equal(time.Unix(100, 500)) {
		t.Fatalf("valid timespec: ok=%v got=%v", ok, got)
	}

	if _, ok := timespecToTime(tsBytes(unix.Timespec{Sec: 0, Nsec: 0})); ok {
		t.Fatal("zero timespec must be rejected")
	}

	// nsPerSec is an untyped constant, so it assigns to Nsec whether it is int64 (amd64) or
	// int32 (386/arm); a literal int64 here would not compile on the 32-bit linux arches.
	if _, ok := timespecToTime(tsBytes(unix.Timespec{Sec: 1, Nsec: nsPerSec})); ok {
		t.Fatal("out-of-range nsec must be rejected")
	}

	if _, ok := timespecToTime(tsBytes(unix.Timespec{Sec: -1, Nsec: 5})); ok {
		t.Fatal("a timestamp before 1970 must be rejected")
	}

	if _, ok := timespecToTime([]byte{0x01, 0x02}); ok {
		t.Fatal("a short buffer must be rejected")
	}
}

// scmTimestampOOB packs a SCM_TIMESTAMPNS control message carrying when, as the kernel
// would deliver it, so replyResult's clock-step handling can be tested without a socket.
func scmTimestampOOB(t *testing.T, when time.Time) []byte {
	t.Helper()

	ts := unix.NsecToTimespec(when.UnixNano())
	sz := binary.Size(ts)

	h := unix.Cmsghdr{Level: unix.SOL_SOCKET, Type: unix.SCM_TIMESTAMPNS}
	h.SetLen(unix.CmsgLen(sz))

	b := make([]byte, unix.CmsgSpace(sz))

	_, err := binary.Encode(b, binary.NativeEndian, h)
	if err != nil {
		t.Fatal(err)
	}

	_, err = binary.Encode(b[unix.CmsgLen(0):], binary.NativeEndian, ts)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// TestResultClockStepFallback pins F5: the kernel timestamp is used only when plausible
// (in [0, monotonic span]); a backward or forward CLOCK_REALTIME step falls back to the
// step-immune monotonic span instead of yielding a negative/huge RTT.
func TestResultClockStepFallback(t *testing.T) {
	tSend := time.Now()
	recvUser := tSend.Add(10 * time.Millisecond) // monotonic span = 10ms.

	cases := []struct {
		name   string
		oob    []byte
		wantMs float64
	}{
		{"plausible kernel ts", scmTimestampOOB(t, tSend.Add(3*time.Millisecond)), 3.0},
		{"forward step (k>span)", scmTimestampOOB(t, tSend.Add(50*time.Millisecond)), 10.0},
		{"backward step (k<0)", scmTimestampOOB(t, tSend.Add(-5*time.Millisecond)), 10.0},
		{"no kernel ts", nil, 10.0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := replyResult(c.oob, recvUser, tSend)
			if !res.IsSuccess() {
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

	// An out-of-range numeric zone must not truncate into a bogus ZoneId; it degrades to 0.
	for _, z := range []string{"-1", "4294967296", "99999999999999999999"} {
		if got := zoneIndex(z); got != 0 {
			t.Errorf("out-of-range zone %q = %d, want 0", z, got)
		}
	}
}

// TestLiteralAddrZone pins F3: a scoped IPv6 literal keeps its zone as an interface index;
// a plain address has zone 0. A literal of another family than resolve_family never gets
// here: Compile refuses it.
func TestLiteralAddrZone(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip("no lo interface")
	}

	ip, zone := literalAddr(netip.MustParseAddr("fe80::1%lo"))
	if ip == nil || zone != lo.Index {
		t.Fatalf("fe80::1%%lo -> ip=%v zone=%d, want zone=%d", ip, zone, lo.Index)
	}

	if _, zone = literalAddr(netip.MustParseAddr("192.0.2.1")); zone != 0 {
		t.Fatalf("192.0.2.1 -> zone=%d, want zone=0", zone)
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

// A zoned source address binds on its zone's interface: the zone is the only thing that
// tells one link's fe80:: address from another's, so dropping it binds nowhere useful.
func TestAddrSockaddrKeepsZone(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip("no loopback interface named lo")
	}

	got, err := addrSockaddr(netip.MustParseAddr("fe80::1%lo"))
	if err != nil {
		t.Fatal(err)
	}

	want, err := ipSockaddr(net.ParseIP("fe80::1"), lo.Index)
	if err != nil {
		t.Fatal(err)
	}

	g, gok := got.(*unix.SockaddrInet6)
	w, wok := want.(*unix.SockaddrInet6)

	if !gok || !wok || *g != *w {
		t.Errorf("sockaddr = %+v, want %+v", got, want)
	}
}
