package probe

import (
	"fmt"
	"strings"
	"testing"
)

func TestCompilePortBoundaries(t *testing.T) {
	for _, c := range []struct {
		number int
		valid  bool
	}{
		{-1, false},
		{0, false},
		{1, true},
		{2, true},
		{65534, true},
		{65535, true},
		{65536, false},
	} {
		port := PortNumber(c.number)
		// Explicit resolved parameters also check that a valid port is preserved.
		for _, params := range []Params{
			TCP{Port: port, Family: FamilyIPv4},
			QUIC{Port: port, Family: FamilyIPv4, ALPN: "h3", Verify: VerifyDisabled},
			RouterOS{
				Host: "router", Port: port, Scheme: "https",
				Username: "user", Password: "secret", Verify: VerifyEnabled,
			},
		} {
			t.Run(fmt.Sprintf("%s/%d", params.method(), c.number), func(t *testing.T) {
				plan, err := Compile(Spec{Addr: "192.0.2.1", Params: params})
				if !c.valid {
					if err == nil || !strings.Contains(err.Error(), "invalid port") {
						t.Fatalf("port %d: %v, want an invalid port error", c.number, err)
					}

					return
				}

				if err != nil {
					t.Fatal(err)
				}

				if got := plan.Params(); got != params {
					t.Fatalf("compiled parameters = %+v, want %+v", got, params)
				}
			})
		}
	}
}

func TestCompileALPNByteBoundaries(t *testing.T) {
	for _, c := range []struct {
		name  string
		alpn  string
		valid bool
	}{
		{"one byte", "x", true},
		{"254 bytes", strings.Repeat("x", 254), true},
		{"255 bytes", strings.Repeat("x", 255), true},
		{"256 bytes", strings.Repeat("x", 256), false},
		{"255 UTF-8 bytes", strings.Repeat("あ", 85), true},
		{"256 UTF-8 bytes", strings.Repeat("あ", 85) + "x", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan, err := Compile(Spec{Addr: "example.com", Params: QUIC{ALPN: c.alpn}})
			if !c.valid {
				if err == nil || !strings.Contains(err.Error(), "ALPN exceeds 255 bytes") {
					t.Fatalf("ALPN: %v, want the byte length error", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			params, ok := plan.Params().(QUIC)
			if !ok || params.ALPN != c.alpn {
				t.Fatalf("compiled parameters = %+v, want ALPN %q", plan.Params(), c.alpn)
			}
		})
	}
}
