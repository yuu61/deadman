package probe

import (
	"net/netip"
	"strings"
	"testing"
)

// A NUL can survive config tokenization but cannot be passed to exec or a socket.
// Compile must reject it before a row starts failing on every round.
func TestCompileRejectsNULInExecutionValues(t *testing.T) {
	for name, spec := range map[string]Spec{
		"netns": {Addr: "192.0.2.1", Params: Netns{Name: "blue\x00bad"}},
		"vrf":   {Addr: "192.0.2.1", Params: VRF{Name: "blue\x00bad"}},
		"ssh user": {
			Addr: "192.0.2.1", Params: SSH{Host: "jump", OS: OSLinux, User: "ops\x00bad"},
		},
		"ssh key": {
			Addr: "192.0.2.1", Params: SSH{Host: "jump", OS: OSLinux, Key: "/key\x00bad"},
		},
		"direct interface": {
			Addr: "192.0.2.1", Params: Direct{Source: SourceInterface("eth\x00bad")},
		},
		"relay interface": {
			Addr: "192.0.2.1", Params: Netns{Name: "blue", Source: SourceInterface("eth\x00bad")},
		},
		"source zone": {
			Addr: "fe80::1", Params: Direct{Source: SourceAddr(netip.MustParseAddr("fe80::2%eth\x00bad"))},
		},
		"target zone": {Addr: "fe80::1%eth\x00bad"},
		"relay zone": {
			Addr: "192.0.2.1", Params: SSH{Host: "fe80::1%eth\x00bad", OS: OSLinux},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(spec)
			if err == nil {
				t.Fatal("Compile accepted a NUL in an execution value")
			}
		})
	}
}

func TestCompileNULCredentialErrorDoesNotRevealKey(t *testing.T) {
	const key = "/private-key\x00suffix"

	_, err := Compile(Spec{Addr: "192.0.2.1", Params: SSH{Host: "jump", OS: OSLinux, Key: key}})
	if err == nil || !strings.Contains(err.Error(), "ssh key") ||
		strings.Contains(err.Error(), "private-key") {
		t.Fatalf("expected a field-specific error without the credential value, got %v", err)
	}
}
