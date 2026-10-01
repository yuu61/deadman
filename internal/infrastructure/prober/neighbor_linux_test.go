//go:build linux

package prober

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNeighborRequestTargetsOneEntry(t *testing.T) {
	for _, address := range []string{"192.0.2.1", "2001:db8::1"} {
		ip := net.ParseIP(address)

		raw, family, err := neighborRequest(7, ip)
		if err != nil {
			t.Fatal(err)
		}

		msgs, err := syscall.ParseNetlinkMessage(raw)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("invalid request: %v, messages=%d", err, len(msgs))
		}

		m := msgs[0]
		if m.Header.Type != unix.RTM_GETNEIGH || m.Header.Flags != unix.NLM_F_REQUEST ||
			m.Header.Seq != 1 {
			t.Fatalf("request must query a single entry: %+v", m.Header)
		}

		var nd unix.NdMsg

		_, err = binary.Decode(m.Data, binary.NativeEndian, &nd)
		if err != nil || nd.Ifindex != 7 || int(nd.Family) != family {
			t.Fatalf("wrong neighbor selector: %+v, %v", nd, err)
		}

		attr := m.Data[unix.SizeofNdMsg:]
		if binary.NativeEndian.Uint16(attr[2:]) != unix.NDA_DST ||
			!net.IP(attr[unix.SizeofRtAttr:]).Equal(ip) {
			t.Fatalf("wrong destination attribute: %x", attr)
		}
	}
}

func neighborMessage(t *testing.T, kind uint16, value any) []byte {
	t.Helper()

	data, err := binary.Append(nil, binary.NativeEndian, value)
	if err != nil {
		t.Fatal(err)
	}

	n := uint64(len(data)) + unix.SizeofNlMsghdr
	if n > math.MaxUint32 {
		t.Fatal("test reply exceeds netlink message length")

		return nil
	}

	header := unix.NlMsghdr{Len: uint32(n), Type: kind, Seq: 1}

	raw, err := binary.Append(nil, binary.NativeEndian, header)
	if err != nil {
		t.Fatal(err)
	}

	return append(raw, data...)
}

func TestNeighborReplyDistinguishesCacheErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  []byte
		want error
	}{
		{"absent", neighborMessage(t, unix.NLMSG_ERROR, -int32(unix.ENOENT)), nil},
		{"permission", neighborMessage(t, unix.NLMSG_ERROR, -int32(unix.EPERM)), unix.EPERM},
		{"interrupted", neighborMessage(t, unix.NLMSG_ERROR, -int32(unix.EINTR)), unix.EINTR},
		{"short error", neighborMessage(t, unix.NLMSG_ERROR, uint16(0)), errors.New("short")},
		{"ack", neighborMessage(t, unix.NLMSG_ERROR, int32(0)), errors.New("ack")},
		{"empty", nil, errors.New("empty")},
		{"truncated", []byte{1, 2, 3}, errors.New("truncated")},
	} {
		t.Run(c.name, func(t *testing.T) {
			mac, state, err := neighborReply(c.raw, unix.AF_INET, 7, net.ParseIP("192.0.2.1"))
			if mac != nil || state != neighMissing {
				t.Fatalf("error response produced a neighbor: %s, %d", mac, state)
			}

			if (err == nil) != (c.want == nil) {
				t.Fatalf("error=%v, want %v", err, c.want)
			}

			var errno syscall.Errno
			if errors.As(c.want, &errno) && !errors.Is(err, errno) {
				t.Fatalf("error=%v, want %v", err, errno)
			}
		})
	}
}

func TestNeighborReplyReadsARPAndNDP(t *testing.T) {
	mac := net.HardwareAddr{0, 1, 2, 3, 4, 5}

	for _, address := range []string{"192.0.2.1", "2001:db8::1"} {
		ip := net.ParseIP(address)

		raw, family, err := neighborRequest(7, ip)
		if err != nil {
			t.Fatal(err)
		}

		for _, state := range []uint16{unix.NUD_REACHABLE, unix.NUD_STALE, unix.NUD_FAILED} {
			// Reuse the selector's destination attribute in a kernel-style reply.
			data := bytes.Clone(raw[unix.SizeofNlMsghdr:])
			nd := unix.NdMsg{Family: data[0], Ifindex: 7, State: state}

			_, err = binary.Encode(data, binary.NativeEndian, nd)
			if err != nil {
				t.Fatal(err)
			}

			attr := unix.RtAttr{Len: unix.SizeofRtAttr + 6, Type: unix.NDA_LLADDR}

			data, err = binary.Append(data, binary.NativeEndian, attr)
			if err != nil {
				t.Fatal(err)
			}

			data = append(data, mac...)
			data = append(data, 0, 0) // rtattr alignment.

			got, st, err := neighborReply(
				neighborMessage(t, unix.RTM_NEWNEIGH, data),
				family,
				7,
				ip,
			)
			if err != nil {
				t.Fatal(err)
			}

			want := neighStale

			switch state {
			case unix.NUD_REACHABLE:
				want = neighReachable
			case unix.NUD_FAILED:
				want = neighMissing
			default:
				// Other usable states are unconfirmed.
			}

			if st != want || (want != neighMissing && !bytes.Equal(got, mac)) {
				t.Fatalf(
					"%s state=%d: MAC=%s state=%d, want %s state=%d",
					address,
					state,
					got,
					st,
					mac,
					want,
				)
			}
		}
	}
}

func TestNeighLookupMissingEntryOnKernel(t *testing.T) {
	iface, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip(err)
	}

	for _, ip := range []net.IP{net.ParseIP("192.0.2.254"), net.ParseIP("2001:db8::ffff")} {
		mac, state, err := neighLookup(t.Context(), iface.Index, ip)
		if err != nil || mac != nil || state != neighMissing {
			t.Fatalf("absent neighbor: MAC=%s state=%d error=%v", mac, state, err)
		}
	}
}

func TestResolveNeighborCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	kicked := false
	start := time.Now()

	_, err := resolveNeighbor(
		ctx,
		func(context.Context) (net.HardwareAddr, neighState, error) { return nil, neighMissing, nil },
		func(context.Context) {
			kicked = true

			cancel()
		},
	)
	if !kicked || !errors.Is(err, context.Canceled) || time.Since(start) >= neighborResolveTimeout {
		t.Fatalf("cancellation: kicked=%v error=%v elapsed=%s", kicked, err, time.Since(start))
	}
}

func TestResolveNeighborFallback(t *testing.T) {
	mac := net.HardwareAddr{0, 1, 2, 3, 4, 5}

	for _, state := range []neighState{neighMissing, neighStale, neighReachable} {
		got, err := resolveNeighbor(t.Context(),
			func(context.Context) (net.HardwareAddr, neighState, error) { return mac, state, nil },
			func(context.Context) {},
		)
		if state == neighMissing {
			if !errors.Is(err, errGatewayUnresolved) {
				t.Fatalf("missing neighbor: error=%v, want unresolved", err)
			}
		} else if err != nil || !bytes.Equal(got, mac) {
			t.Fatalf("usable neighbor: MAC=%s error=%v", got, err)
		}
	}
}

func TestResolveNeighborPropagatesLookupError(t *testing.T) {
	_, err := resolveNeighbor(
		t.Context(),
		func(context.Context) (net.HardwareAddr, neighState, error) { return nil, neighMissing, unix.EPERM },
		func(context.Context) { t.Fatal("cache error must not trigger neighbor resolution") },
	)
	if !errors.Is(err, unix.EPERM) {
		t.Fatalf("error=%v, want permission failure", err)
	}
}
