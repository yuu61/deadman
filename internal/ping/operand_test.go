package ping

import (
	"strings"
	"testing"
)

// A config value placed as a bare operand in a subprocess argv must be rejected when it
// starts with '-', or the spawned tool parses it as an option. The headline case is an
// ssh relay of "-oProxyCommand=..." (arbitrary local command execution); the same seam
// exists for netns/vrf names, the snmp relay, and every mode's destination address. New
// must return an error (the TUI degrades such a target to a permanent failure glyph).
func TestNewRejectsOptionLikeOperand(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{
			"ssh_relay_proxycommand",
			Spec{Addr: "1.2.3.4", Relay: map[string]string{"relay": "-oProxyCommand=touch /tmp/x"}},
		},
		{
			"netns_name",
			Spec{Addr: "1.2.3.4", Relay: map[string]string{"via": "netns", "relay": "-x"}},
		},
		{
			"vrf_name",
			Spec{Addr: "1.2.3.4", Relay: map[string]string{"via": "vrf", "relay": "-x"}},
		},
		{
			"snmp_relay",
			Spec{
				Addr:  "1.2.3.4",
				Relay: map[string]string{"via": "snmp", "relay": "-x", "community": "public"},
			},
		},
		{
			"ssh_address",
			Spec{Addr: "-8", Relay: map[string]string{"relay": "h"}},
		},
		{
			"hping_address",
			Spec{Addr: "-8", TCP: "dstport:80"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.spec)
			if err == nil {
				t.Errorf("New(%v) = nil error, want rejection of an option-like operand", c.spec)
			}
		})
	}
}

// A leading-'-' check must not reject legitimate hosts/addresses, nor values consumed
// as a preceding flag's argument (community/user/key/source), which are not operands.
func TestNewAcceptsValidOperands(t *testing.T) {
	specs := []Spec{
		{Addr: "1.2.3.4", Relay: map[string]string{"relay": "h", "user": "u", "key": "k"}},
		{
			Addr: "1.2.3.4",
			Relay: map[string]string{
				"via": "snmp", "relay": "h", "community": "-weird-but-valid",
			},
		},
		{Addr: "example.com", TCP: "dstport:80"},
	}
	for _, s := range specs {
		_, err := New(s)
		if err != nil {
			t.Errorf("New(%v) errored on a valid spec: %v", s.Relay, err)
		}
	}
}

// validateOperands only rejects a leading '-' and names the offending value.
func TestValidateOperands(t *testing.T) {
	err := validateOperands("10.0.0.1", "1.2.3.4")
	if err != nil {
		t.Errorf("validateOperands rejected valid operands: %v", err)
	}

	err = validateOperands("h", "-oProxyCommand=x")
	if err == nil {
		t.Fatal("validateOperands accepted an option-like value")
	}

	if !strings.Contains(err.Error(), "-oProxyCommand=x") {
		t.Errorf("error %q does not name the offending value", err)
	}
}
