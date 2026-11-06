package prober

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestRelayPingOptions(t *testing.T) {
	for _, tc := range []struct {
		os   probe.OS
		addr string
		want []string
	}{
		{probe.OSLinux, "192.0.2.1", []string{
			"env", "LC_ALL=C", "ping", "-c", "1", "-W", "1",
			"-s", "8972", "-M", "do", "192.0.2.1",
		}},
		{probe.OSLinux, "2001:db8::1", []string{
			"env", "LC_ALL=C", "ping", "-6", "-c", "1", "-W", "1",
			"-s", "8972", "-M", "do", "2001:db8::1",
		}},
		{probe.OSDarwin, "192.0.2.1", []string{
			"env", "LC_ALL=C", "ping", "-c", "1", "-W", "1000",
			"-s", "8972", "-D", "192.0.2.1",
		}},
		{probe.OSDarwin, "2001:db8::1", []string{
			"env", "LC_ALL=C", "ping6", "-c", "1",
			"-s", "8972", "-b", "49152", "-D", "2001:db8::1",
		}},
		{probe.OSFreeBSD, "2001:db8::1", []string{
			"env", "LC_ALL=C", "ping", "-6", "-c", "1", "-W", "1000",
			"-s", "8972", "-b", "49152", "-D", "2001:db8::1",
		}},
		{probe.OSJUNOS, "192.0.2.1", []string{
			"ping", "192.0.2.1", "count", "1", "inet", "wait", "1",
			"size", "8972", "do-not-fragment", "interface", "ge-0/0/0.0", "routing-instance", "MGMT",
		}},
		{probe.OSJUNOS, "2001:db8::1", []string{
			"ping", "2001:db8::1", "count", "1", "inet6", "wait", "1",
			"size", "8972", "interface", "ge-0/0/0.0", "routing-instance", "MGMT",
		}},
	} {
		// Compile refuses DF for a JUNOS IPv6 ping, so that case checks its argv without it.
		df := tc.os != probe.OSJUNOS || !strings.Contains(tc.addr, ":")

		params := probe.SSH{
			Host: "r", OS: tc.os,
			Echo: probe.EchoOptions{Size: probe.PayloadBytes(8972), DF: df},
		}
		if tc.os == probe.OSJUNOS {
			params.Interface = probe.CLIWordOf("ge-0/0/0.0")
			params.Instance = probe.CLIWordOf("MGMT")
		}

		p := subprocessFromPlan(t, probe.Spec{Addr: tc.addr, Params: params})

		got := p.buildArgs()[len(p.wrapper):]
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s %s: argv = %q, want %q", tc.os, tc.addr, got, tc.want)
		}
	}
}

func TestBSDIPv6RelayPayload(t *testing.T) {
	for _, os := range []probe.OS{probe.OSDarwin, probe.OSFreeBSD} {
		smallest := probe.PayloadBytes(probe.RelayMinPayload)
		for _, size := range []probe.PayloadSize{{}, probe.PayloadBytes(56), smallest} {
			p := subprocessFromPlan(t, probe.Spec{Addr: "2001:db8::1", Params: probe.SSH{
				Host: "relay", OS: os, Echo: probe.EchoOptions{Size: size},
			}})
			args := p.buildArgs()

			want := []string{"-s", strconv.Itoa(p.echo.Size.Bytes()), "2001:db8::1"}
			if !slices.Equal(args[len(args)-len(want):], want) {
				t.Errorf("%s size=%+v: argv = %q, want suffix %q", os, size, args, want)
			}
		}
	}
}

func TestBSDIPv6RelayBufferWithoutDF(t *testing.T) {
	for _, os := range []probe.OS{probe.OSDarwin, probe.OSFreeBSD} {
		for _, tc := range []struct {
			size   int
			buffer string
		}{
			{8972, "49152"},
			{65507, "131150"},
		} {
			p := subprocessFromPlan(t, probe.Spec{Addr: "2001:db8::1", Params: probe.SSH{
				Host: "relay", OS: os, Echo: probe.EchoOptions{Size: probe.PayloadBytes(tc.size)},
			}})
			args := p.buildArgs()

			want := []string{"-s", strconv.Itoa(tc.size), "-b", tc.buffer, "2001:db8::1"}
			if !slices.Equal(args[len(args)-len(want):], want) {
				t.Errorf("%s: argv = %q, want suffix %q", os, args, want)
			}
		}
	}
}

func TestJUNOSSourceArgument(t *testing.T) {
	plan := compiled(t, probe.Spec{Addr: "192.0.2.1", Params: probe.SSH{
		Host: "r", OS: probe.OSJUNOS, Source: probe.SourceAddr(netip.MustParseAddr("192.0.2.2")),
	}})

	built, err := New(plan, "row#1")
	if err != nil {
		t.Fatal(err)
	}

	p, ok := built.(*subprocessPinger)
	if !ok {
		t.Fatalf("built %T", built)
	}

	got := p.buildArgs()
	if !slices.Equal(got[len(got)-2:], []string{"source", "192.0.2.2"}) {
		t.Fatalf("JUNOS source = %q", got)
	}
}

func TestNamespaceEchoOptions(t *testing.T) {
	echo := probe.EchoOptions{Size: probe.PayloadBytes(probe.RelayMinPayload), DF: true}
	for _, params := range []probe.Params{
		probe.Netns{Name: "ns", Echo: echo}, probe.VRF{Name: "blue", Echo: echo},
	} {
		p := subprocessFromPlan(t, probe.Spec{Addr: "192.0.2.1", Params: params})
		got := p.buildArgs()[len(p.wrapper):]

		want := []string{"ping", "-c", "1", "-W", "1", "-s", "16", "-M", "do", "192.0.2.1"}
		if !slices.Equal(got, want) {
			t.Fatalf("argv = %q, want %q", got, want)
		}
	}
}

func TestSSHOverallTimeout(t *testing.T) {
	for _, tc := range []struct {
		os      probe.OS
		addr    string
		timeout probe.Timeout
		want    time.Duration
	}{
		{probe.OSLinux, "192.0.2.1", probe.Timeout{}, 5 * time.Second},
		{probe.OSJUNOS, "192.0.2.1", probe.Timeout{}, 5 * time.Second},
		{probe.OSDarwin, "2001:db8::1", probe.Timeout{}, 15 * time.Second},
		{probe.OSJUNOS, "192.0.2.1", probe.TimeoutAfter(7500 * time.Millisecond), 7500 * time.Millisecond},
		{probe.OSDarwin, "2001:db8::1", probe.TimeoutAfter(2 * time.Second), 2 * time.Second},
	} {
		p := subprocessFromPlan(t, probe.Spec{Addr: tc.addr, Params: probe.SSH{
			Host: "r", OS: tc.os, Timeout: tc.timeout,
		}})
		if p.timeout != tc.want {
			t.Errorf("%s %s: timeout = %s, want %s", tc.os, tc.addr, p.timeout, tc.want)
		}
	}
}

func subprocessFromPlan(t *testing.T, spec probe.Spec) *subprocessPinger {
	t.Helper()

	p, err := New(compiled(t, spec), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	subprocess, ok := p.(*subprocessPinger)
	if !ok {
		t.Fatalf("built %T", p)
	}

	return subprocess
}
