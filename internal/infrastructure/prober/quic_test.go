package prober

import (
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// The QUIC defaults are the contract's sharp edges: verify defaults OFF (the opposite of
// routeros), the port defaults to 443, the ALPN to h3, and the SNI falls back to a
// hostname Addr but not to an IP literal — including a zoned IPv6 literal. probe.Compile
// fills them in; this is a white-box check that they reach the tls.Config — no network —
// so the plan and the probe cannot silently drift.
func TestNewQUICPinger(t *testing.T) {
	cases := []struct {
		name         string
		spec         probe.Spec
		wantHost     string
		wantPort     string
		wantInsecure bool
		wantALPN     string
		wantSNI      string
	}{
		{
			name: "defaults_ip",
			spec: probe.Spec{
				Addr:   "1.2.3.4",
				Params: probe.QUIC{},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: true, // verify defaults OFF.
			wantALPN:     "h3",
			wantSNI:      "", // a bare IP literal sends no SNI by default.
		},
		{
			name: "hostname_sni_fallback",
			spec: probe.Spec{
				Addr:   "example.com",
				Params: probe.QUIC{},
			},
			wantHost:     "example.com",
			wantPort:     "443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "example.com", // a hostname Addr becomes the SNI.
		},
		{
			// A zoned IPv6 literal is still a literal: no SNI, zone not leaked.
			name: "zoned_ipv6_literal_no_sni",
			spec: probe.Spec{
				Addr:   "fe80::1%eth0",
				Params: probe.QUIC{},
			},
			wantHost:     "fe80::1%eth0",
			wantPort:     "443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "verify_on_secures",
			spec: probe.Spec{
				Addr:   "1.2.3.4",
				Params: probe.QUIC{Verify: probe.VerifyEnabled},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: false, // only an explicit truthy value verifies.
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "verify_off_stays_insecure",
			spec: probe.Spec{
				Addr:   "1.2.3.4",
				Params: probe.QUIC{Verify: probe.VerifyDisabled},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "explicit_port_alpn_sni",
			spec: probe.Spec{
				Addr: "1.2.3.4",
				Params: probe.QUIC{
					ALPN: "hq-interop",
					SNI:  "host.internal",
					Port: probe.PortNumber(8443),
				},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "8443",
			wantInsecure: true,
			wantALPN:     "hq-interop",
			wantSNI:      "host.internal",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pinger, err := New(compiled(t, c.spec), "row#1")
			if err != nil {
				t.Fatalf("New error: %v", err)
			}

			p, ok := pinger.(*quicPinger)
			if !ok {
				t.Fatalf("newQUICPinger returned %T, want *quicPinger", pinger)
			}

			if host := p.dest.String(); host != c.wantHost || p.port != c.wantPort {
				t.Errorf("host:port = %q:%q, want %q:%q", host, p.port, c.wantHost, c.wantPort)
			}

			if p.tlsConfig.InsecureSkipVerify != c.wantInsecure {
				t.Errorf(
					"InsecureSkipVerify = %v, want %v",
					p.tlsConfig.InsecureSkipVerify,
					c.wantInsecure,
				)
			}

			if len(p.tlsConfig.NextProtos) != 1 || p.tlsConfig.NextProtos[0] != c.wantALPN {
				t.Errorf("NextProtos = %v, want [%q]", p.tlsConfig.NextProtos, c.wantALPN)
			}

			if p.tlsConfig.ServerName != c.wantSNI {
				t.Errorf("ServerName = %q, want %q", p.tlsConfig.ServerName, c.wantSNI)
			}
		})
	}
}

// resolve_family pins the QUIC dial's hostname resolution to a family (like direct
// ICMP); unset lets the resolver choose ("ip").
func TestNewQUICPingerResolveFamily(t *testing.T) {
	for family, want := range map[probe.Family]string{
		probe.FamilyUnknown: "ip",
		probe.FamilyIPv4:    "ip4",
		probe.FamilyIPv6:    "ip6",
	} {
		p, err := New(
			compiled(
				t,
				probe.Spec{Addr: "example.com", Params: probe.QUIC{Family: family}},
			),
			"row#1",
		)
		if err != nil {
			t.Fatal(err)
		}

		qp, ok := p.(*quicPinger)
		if !ok || qp.network != want {
			t.Fatalf("family %v: %+v", family, p)
		}
	}
}

// resolveAddr returns an IP literal (including a zoned IPv6 literal) unchanged and adds
// the port, so a literal target performs no DNS inside the RTT window. Hostname
// resolution needs the network and is covered by the manual suite.
func TestQUICResolveAddrLiteral(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":      "1.2.3.4:443",
		"2001:db8::1":  "[2001:db8::1]:443",
		"fe80::1%eth0": "[fe80::1%eth0]:443",
	}

	for addr, want := range cases {
		pinger, err := New(
			compiled(t, probe.Spec{Addr: addr, Params: probe.QUIC{}}), "row#1",
		)
		if err != nil {
			t.Fatalf("%s: New error: %v", addr, err)
		}

		qp, ok := pinger.(*quicPinger)
		if !ok {
			t.Fatalf("%s: newQUICPinger returned %T, want *quicPinger", addr, pinger)
		}

		got, err := qp.resolveAddr(t.Context())
		if err != nil {
			t.Errorf("%s: resolveAddr error: %v", addr, err)

			continue
		}

		if got != want {
			t.Errorf("%s: resolveAddr = %q, want %q", addr, got, want)
		}
	}
}
