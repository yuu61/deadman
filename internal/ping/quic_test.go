package ping

import (
	"context"
	"testing"
)

// newQUICPinger's defaults are the contract's sharp edges: verify defaults OFF (the
// opposite of routeros, so copying that switch verbatim would be a bug), the port
// defaults to 443, the ALPN to h3, and the SNI falls back to a hostname Addr but not to
// an IP literal — including a zoned IPv6 literal, which net.ParseIP would misclassify as
// a hostname. This is a white-box check of the resolved tls.Config — no network — so the
// defaults cannot silently drift.
func TestNewQUICPinger(t *testing.T) {
	cases := []struct {
		name         string
		spec         Spec
		wantHost     string
		wantPort     string
		wantInsecure bool
		wantALPN     string
		wantSNI      string
	}{
		{
			name:         "defaults_ip",
			spec:         Spec{Addr: "1.2.3.4", Relay: map[string]string{"via": "quic"}},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: true, // verify defaults OFF.
			wantALPN:     "h3",
			wantSNI:      "", // a bare IP literal sends no SNI by default.
		},
		{
			name:         "hostname_sni_fallback",
			spec:         Spec{Addr: "example.com", Relay: map[string]string{"via": "quic"}},
			wantHost:     "example.com",
			wantPort:     "443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "example.com", // a hostname Addr becomes the SNI.
		},
		{
			// A zoned IPv6 literal is still a literal: no SNI, zone not leaked.
			name:         "zoned_ipv6_literal_no_sni",
			spec:         Spec{Addr: "fe80::1%eth0", Relay: map[string]string{"via": "quic"}},
			wantHost:     "fe80::1%eth0",
			wantPort:     "443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "verify_on_secures",
			spec: Spec{
				Addr:  "1.2.3.4",
				Relay: map[string]string{"via": "quic", "verify": "on"},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: false, // only an explicit truthy value verifies.
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "verify_off_stays_insecure",
			spec: Spec{
				Addr:  "1.2.3.4",
				Relay: map[string]string{"via": "quic", "verify": "off"},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "verify_garbage_stays_insecure",
			spec: Spec{
				Addr:  "1.2.3.4",
				Relay: map[string]string{"via": "quic", "verify": "maybe"},
			},
			wantHost:     "1.2.3.4",
			wantPort:     "443",
			wantInsecure: true, // an unrecognized value must not accidentally secure.
			wantALPN:     "h3",
			wantSNI:      "",
		},
		{
			name: "explicit_port_alpn_sni",
			spec: Spec{
				Addr: "1.2.3.4",
				Relay: map[string]string{
					"via": "quic", "port": "8443", "alpn": "hq-interop", "sni": "host.internal",
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
			pinger, err := newQUICPinger(c.spec)
			if err != nil {
				t.Fatalf("newQUICPinger error: %v", err)
			}

			p, ok := pinger.(*quicPinger)
			if !ok {
				t.Fatalf("newQUICPinger returned %T, want *quicPinger", pinger)
			}

			if p.host != c.wantHost || p.port != c.wantPort {
				t.Errorf("host:port = %q:%q, want %q:%q", p.host, p.port, c.wantHost, c.wantPort)
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

// An invalid port= must fail at construction, so model.go turns it into a startup
// warning and a permanent-failure row rather than a silent, undebuggable X.
func TestNewQUICPingerInvalidPort(t *testing.T) {
	for _, port := range []string{"abc", "0", "-1", "65536", "99999", " 443"} {
		_, err := newQUICPinger(
			Spec{Addr: "1.2.3.4", Relay: map[string]string{"via": "quic", "port": port}},
		)
		if err == nil {
			t.Errorf("port=%q: expected a construction error, got nil", port)
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
		pinger, err := newQUICPinger(Spec{Addr: addr, Relay: map[string]string{"via": "quic"}})
		if err != nil {
			t.Fatalf("%s: newQUICPinger error: %v", addr, err)
		}

		qp, ok := pinger.(*quicPinger)
		if !ok {
			t.Fatalf("%s: newQUICPinger returned %T, want *quicPinger", addr, pinger)
		}

		got, err := qp.resolveAddr(context.Background())
		if err != nil {
			t.Errorf("%s: resolveAddr error: %v", addr, err)

			continue
		}

		if got != want {
			t.Errorf("%s: resolveAddr = %q, want %q", addr, got, want)
		}
	}
}
