package prober

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"testing"

	"github.com/quic-go/quic-go"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestQUICErrorsDoNotInventPacketLoss(t *testing.T) {
	for _, err := range []error{
		&net.OpError{Op: "listen", Net: "udp", Err: errors.New("too many open files")},
		&tls.CertificateVerificationError{Err: errors.New("unknown certificate")},
		&quic.TransportError{},
		context.Canceled,
	} {
		target := monitor.NewTarget("id", "host", "192.0.2.1")
		target.Consume(quicFailure(fmt.Errorf("dial: %w", err)))

		got := target.Snapshot()
		if got.State != monitor.Unknown || got.Snt != 0 || got.Loss != 0 {
			t.Errorf("%T invented observation: %+v", err, got)
		}
	}

	for _, err := range []error{&quic.HandshakeTimeoutError{}, context.DeadlineExceeded} {
		if got := quicFailure(err); got.Code != probe.Failed {
			t.Errorf("timeout: %+v", got)
		}
	}
}

// The command must use the compiled family even when local DNS cannot resolve the name.
func TestRelayUsesCompiledFamilyWithoutLocalResolution(t *testing.T) {
	for _, c := range []struct {
		os     probe.OS
		family probe.Family
		want   []string
	}{
		{probe.OSLinux, probe.FamilyIPv4, []string{"ping", "-4", "-c", "1", "-W", "1"}},
		{probe.OSLinux, probe.FamilyIPv6, []string{"ping", "-6", "-c", "1", "-W", "1"}},
		{probe.OSDarwin, probe.FamilyIPv6, []string{"ping6", "-c", "1"}},
		{probe.OSFreeBSD, probe.FamilyIPv4, []string{"ping", "-4", "-c", "1", "-W", "1000"}},
	} {
		plan := compiled(
			t,
			probe.Spec{
				Addr:   "remote-only.invalid",
				Params: probe.SSH{Host: "jump", OS: c.os, Family: c.family},
			},
		)

		p, err := New(plan, "row#1")
		if err != nil {
			t.Fatal(err)
		}

		sp, ok := p.(*subprocessPinger)
		if !ok {
			t.Fatalf("unexpected %T", p)
		}

		args := sp.buildArgs()

		want := append(slices.Clone(c.want), "remote-only.invalid")
		if !slices.Equal(args[len(args)-len(want):], want) {
			t.Errorf("args=%v want suffix %v", args, want)
		}
	}
}

// The relay's ping gets the source as the plan holds it: an interface by name, an address
// with its zone. The adapter never re-reads the text to decide which it is.
func TestRelayPassesTypedSource(t *testing.T) {
	for _, c := range []struct {
		spec probe.Spec
		want []string
	}{
		{
			probe.Spec{
				Addr: "2001:db8::1",
				Params: probe.SSH{
					Host:   "jump",
					OS:     probe.OSDarwin,
					Source: probe.SourceAddr(netip.MustParseAddr("fe80::9%en0")),
				},
			},
			[]string{"-S", "fe80::9%en0", "2001:db8::1"},
		},
		{
			probe.Spec{
				Addr:   "192.0.2.1",
				Params: probe.Netns{Name: "ns", Source: probe.SourceInterface("veth1")},
			},
			[]string{"-I", "veth1", "192.0.2.1"},
		},
	} {
		p, err := New(compiled(t, c.spec), "row#1")
		if err != nil {
			t.Fatal(err)
		}

		sp, ok := p.(*subprocessPinger)
		if !ok {
			t.Fatalf("unexpected %T", p)
		}

		if args := sp.buildArgs(); !slices.Equal(args[len(args)-len(c.want):], c.want) {
			t.Errorf("args=%v want suffix %v", args, c.want)
		}
	}
}

// An IPv4-mapped target reaches every tool as IPv4: a relay's `ping -4` refuses
// ::ffff:192.0.2.1 ("Address family for hostname not supported"), and the snmp probe
// would hand the agent an IPv6 address.
func TestIPv4MappedTargetsReachToolsAsIPv4(t *testing.T) {
	const mapped, want = "::ffff:192.0.2.1", "192.0.2.1"

	for _, params := range []probe.Params{
		probe.SSH{Host: "jump", OS: probe.OSLinux},
		probe.Netns{Name: "ns"},
		probe.TCP{Port: probe.PortNumber(80)},
		probe.SNMP{Host: "agent", Community: "public"},
	} {
		p, err := New(compiled(t, probe.Spec{Addr: mapped, Params: params}), "row#1")
		if err != nil {
			t.Fatal(err)
		}

		var got string

		switch p := p.(type) {
		case *subprocessPinger:
			args := p.buildArgs()
			got = args[len(args)-1]
		case *tcpPinger:
			got = p.dest.String()
		case *snmpPinger:
			got = p.dest.String()
		default:
			t.Fatalf("unexpected %T", p)
		}

		if got != want {
			t.Errorf("%T: target %q, want %q", params, got, want)
		}
	}
}

// Every relay dialect Compile gives a source has the flag that passes it, so the source
// reaches the relay's ping instead of being dropped.
func TestEverySourceCompileAdmitsHasAFlag(t *testing.T) {
	sources := []probe.Source{
		probe.SourceAddr(netip.MustParseAddr("192.0.2.9")),
		probe.SourceInterface("eth1"),
	}

	for _, os := range []probe.OS{probe.OSLinux, probe.OSDarwin, probe.OSFreeBSD} {
		for _, src := range sources {
			plan, err := probe.Compile(probe.Spec{
				Addr:   "192.0.2.1",
				Params: probe.SSH{Host: "jump", OS: os, Source: src},
			})
			if err != nil {
				continue // Compile refuses this source on this dialect.
			}

			p, err := New(plan, "row#1")
			if err != nil {
				t.Fatal(err)
			}

			sp, ok := p.(*subprocessPinger)
			if !ok {
				t.Fatalf("unexpected %T", p)
			}

			args := sp.buildArgs()
			if flag, ok := sourceFlags[os]; !ok || !slices.Contains(args, flag) ||
				!slices.Contains(args, src.String()) {
				t.Errorf("%s relay with source %s ran %v", os, src, args)
			}
		}
	}
}

// Through ssh the relay's shell splits the ping's command line again, so every word is
// quoted for it: a name with a glob reaches ping as one word, and a command substitution
// is never run on the relay. (A name with a space never gets here: Compile refuses it.)
func TestSSHQuotesTheRemoteCommand(t *testing.T) {
	for _, c := range []struct {
		word, want string
		family     probe.Family // a name needs one; a literal fixes its own.
	}{
		{"router.example", "router.example", probe.FamilyIPv4},
		{"fe80::1%eth0", "fe80::1%eth0", probe.FamilyUnknown},
		{"h?", "'h?'", probe.FamilyIPv4},
		{"h*", "'h*'", probe.FamilyIPv4},
		{"$(reboot)", "'$(reboot)'", probe.FamilyIPv4},
		{"it's", `'it'\''s'`, probe.FamilyIPv4},
	} {
		plan := compiled(t, probe.Spec{
			Addr:   c.word,
			Params: probe.SSH{Host: "jump", OS: probe.OSLinux, Family: c.family},
		})

		p, err := New(plan, "row#1")
		if err != nil {
			t.Fatal(err)
		}

		sp, ok := p.(*subprocessPinger)
		if !ok {
			t.Fatalf("unexpected %T", p)
		}

		if args := sp.buildArgs(); args[len(args)-1] != c.want {
			t.Errorf("%q reached the relay as %q, want %q", c.word, args[len(args)-1], c.want)
		}
	}
}
