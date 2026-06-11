package ping

import "testing"

// newQUICPinger's defaults are the contract's sharp edges: verify defaults OFF (the
// opposite of routeros, so copying that switch verbatim would be a bug), the port
// defaults to 443, the ALPN to h3, and the SNI falls back to a hostname Addr but not
// to a bare IP literal. This is a white-box check of the resolved tls.Config — no
// network — so the defaults cannot silently drift.
func TestNewQUICPinger(t *testing.T) {
	cases := []struct {
		name         string
		spec         Spec
		wantAddr     string
		wantInsecure bool
		wantALPN     string
		wantSNI      string
	}{
		{
			name:         "defaults_ip",
			spec:         Spec{Addr: "1.2.3.4", Relay: map[string]string{"via": "quic"}},
			wantAddr:     "1.2.3.4:443",
			wantInsecure: true, // verify defaults OFF.
			wantALPN:     "h3",
			wantSNI:      "", // a bare IP literal sends no SNI by default.
		},
		{
			name:         "hostname_sni_fallback",
			spec:         Spec{Addr: "example.com", Relay: map[string]string{"via": "quic"}},
			wantAddr:     "example.com:443",
			wantInsecure: true,
			wantALPN:     "h3",
			wantSNI:      "example.com", // a hostname Addr becomes the SNI.
		},
		{
			name: "verify_on_secures",
			spec: Spec{
				Addr:  "1.2.3.4",
				Relay: map[string]string{"via": "quic", "verify": "on"},
			},
			wantAddr:     "1.2.3.4:443",
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
			wantAddr:     "1.2.3.4:443",
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
			wantAddr:     "1.2.3.4:443",
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
			wantAddr:     "1.2.3.4:8443",
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

			if p.addr != c.wantAddr {
				t.Errorf("addr = %q, want %q", p.addr, c.wantAddr)
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
