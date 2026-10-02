package prober

import (
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// A config value placed as a bare operand in a subprocess argv must be rejected when it
// starts with '-', or the spawned tool parses it as an option. The headline case is an
// ssh relay of "-oProxyCommand=..." (arbitrary local command execution); the same seam
// exists for netns/vrf names and every subprocess mode's destination address. New
// must return an error (the TUI degrades such a target to a permanent failure glyph).
func TestNewRejectsOptionLikeOperand(t *testing.T) {
	cases := []struct {
		name string
		spec probe.Spec
	}{
		{
			"ssh_relay_proxycommand",
			probe.Spec{
				Addr:   "1.2.3.4",
				Params: probe.SSH{Host: "-oProxyCommand=/tmp/x", OS: probe.OSLinux},
			},
		},
		{
			"netns_name",
			probe.Spec{
				Addr:   "1.2.3.4",
				Params: probe.Netns{Name: "-x"},
			},
		},
		{
			"vrf_name",
			probe.Spec{
				Addr:   "1.2.3.4",
				Params: probe.VRF{Name: "-x"},
			},
		},
		{
			"ssh_address",
			probe.Spec{
				Addr:   "-8",
				Params: probe.SSH{Host: "h", OS: probe.OSLinux, Family: probe.FamilyIPv4},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(compiled(t, c.spec), "row#1")
			if err == nil {
				t.Errorf("New(%v) = nil error, want rejection of an option-like operand", c.spec)
			}
		})
	}
}

// A leading-'-' check must not reject legitimate hosts/addresses, nor values consumed
// as a preceding flag's argument (user/key/source), which are not operands.
func TestNewAcceptsValidOperands(t *testing.T) {
	specs := []probe.Spec{
		{
			Addr:   "1.2.3.4",
			Params: probe.SSH{Host: "h", OS: probe.OSLinux, User: "u", Key: "k"},
		},
		{
			Addr:   "example.com",
			Params: probe.TCP{Port: probe.PortNumber(80)},
		},
		{
			Addr:   "-8",
			Params: probe.TCP{Port: probe.PortNumber(80)},
		},
	}
	for _, s := range specs {
		_, err := New(compiled(t, s), "row#1")
		if err != nil {
			t.Errorf("New(%v) errored on a valid spec: %v", s.Params, err)
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
