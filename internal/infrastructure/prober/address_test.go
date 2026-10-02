package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// useProbeDNS answers only on loopback. The tests using it must remain sequential:
// the adapters resolve through net.DefaultResolver.
func useProbeDNS(
	t *testing.T,
	addresses []netip.Addr,
	beforeReply func(context.Context) error,
) <-chan dnsmessage.Question {
	t.Helper()

	var listen net.ListenConfig

	socket, err := listen.ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	const queryBuffer = 16

	queries := make(chan dnsmessage.Question, queryBuffer)
	done := make(chan error, 1)

	go func() { done <- serveProbeDNS(ctx, socket, addresses, beforeReply, queries) }()

	original := net.DefaultResolver

	var dialer net.Dialer

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, socket.LocalAddr().String())
		},
	}

	t.Cleanup(func() {
		cancel()
		discard(socket.Close())

		serveErr := <-done
		if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) &&
			!errors.Is(serveErr, context.Canceled) {
			t.Error(serveErr)
		}

		net.DefaultResolver = original
	})

	return queries
}

func serveProbeDNS(
	ctx context.Context,
	socket net.PacketConn,
	addresses []netip.Addr,
	beforeReply func(context.Context) error,
	queries chan<- dnsmessage.Question,
) error {
	const packetSize = 4096

	packet := make([]byte, packetSize)

	for {
		n, from, err := socket.ReadFrom(packet)
		if err != nil {
			return fmt.Errorf("read DNS query: %w", err)
		}

		var request dnsmessage.Message

		err = request.Unpack(packet[:n])
		if err != nil {
			return fmt.Errorf("unpack DNS query: %w", err)
		}

		for i := range request.Questions {
			select {
			case queries <- request.Questions[i]:
			default:
			}
		}

		if beforeReply != nil {
			err = beforeReply(ctx)
			if err != nil {
				return err
			}
		}

		response := probeDNSAnswer(request, addresses)

		data, err := response.Pack()
		if err != nil {
			return fmt.Errorf("pack DNS answer: %w", err)
		}

		_, err = socket.WriteTo(data, from)
		if err != nil {
			return fmt.Errorf("write DNS answer: %w", err)
		}
	}
}

func probeDNSAnswer(request dnsmessage.Message, addresses []netip.Addr) dnsmessage.Message {
	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID: request.ID, Response: true, Authoritative: true,
			RecursionDesired: request.RecursionDesired, RecursionAvailable: true,
		},
		Questions: request.Questions,
	}

	for i := range request.Questions {
		q := &request.Questions[i]

		for _, addr := range addresses {
			var body dnsmessage.ResourceBody

			switch {
			case q.Type == dnsmessage.TypeA && addr.Is4():
				body = &dnsmessage.AResource{A: addr.As4()}
			case q.Type == dnsmessage.TypeAAAA && addr.Is6():
				body = &dnsmessage.AAAAResource{AAAA: addr.As16()}
			default:
				continue
			}

			response.Answers = append(response.Answers, dnsmessage.Resource{
				Header: dnsmessage.ResourceHeader{
					Name:  q.Name,
					Type:  q.Type,
					Class: dnsmessage.ClassINET,
				},
				Body: body,
			})
		}
	}

	return response
}

func TestResolveProbeAddressFamilyAndFirstAnswer(t *testing.T) {
	for _, c := range []struct {
		network string
		want    string
		query   dnsmessage.Type
	}{
		{networkIPv4, "127.0.0.1:443", dnsmessage.TypeA},
		{networkIPv6, "[::1]:443", dnsmessage.TypeAAAA},
	} {
		t.Run(c.network, func(t *testing.T) {
			queries := useProbeDNS(t, []netip.Addr{
				netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2"),
				netip.MustParseAddr("::1"), netip.MustParseAddr("::2"),
			}, nil)
			dest := compiled(t, probe.Spec{Addr: "monitor.example.invalid."}).Destination()

			got, err := resolveProbeAddress(t.Context(), dest, "443", c.network)
			if err != nil || got != c.want {
				t.Fatalf("resolved endpoint = %q, %v; want %q", got, err, c.want)
			}

			select {
			case q := <-queries:
				if q.Type != c.query || q.Name.String() != "monitor.example.invalid." {
					t.Fatalf("DNS query = %+v, want %v for the target", q, c.query)
				}
			default:
				t.Fatal("hostname was not resolved through DNS")
			}

			for len(queries) > 0 {
				if q := <-queries; q.Type != c.query {
					t.Fatalf("pinned family queried %v, want only %v", q.Type, c.query)
				}
			}
		})
	}
}
