package config

import (
	"strings"
	"testing"
)

// A UTF-8 BOM (U+FEFF) on the first line — routinely emitted by Windows editors — must
// be stripped, or it sticks to the first token: a "scale" directive becomes a phantom
// target and a host's name is garbled.
func TestParseConfigStripsBOM(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader("\ufeffscale 5\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Scale != 5 {
		t.Errorf("Scale = %g, want 5 (BOM must not block the first-line directive)", cfg.Scale)
	}

	if len(cfg.Targets) != 1 {
		t.Fatalf("got %d targets, want 1 (no phantom from the BOM line)", len(cfg.Targets))
	}

	if got := cfg.Targets[0]; got.Name != "host" || got.Addr != "1.2.3.4" {
		t.Errorf("target = {Name:%q Addr:%q}, want host/1.2.3.4", got.Name, got.Addr)
	}
}

// netns= is not a relay key (the netns mode is via=netns + relay=NAME), so a `netns=`
// token is a typo and must surface as an ignored stray token rather than being silently
// swallowed into the relay map.
func TestParseConfigNetnsIsStrayToken(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader("x 1.2.3.4 netns=ns1 via=netns\n"))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(cfg.Targets))
	}

	spec := cfg.Targets[0]
	if _, ok := spec.Relay["netns"]; ok {
		t.Error("netns= was routed into Relay; want it dropped as a stray token")
	}

	found := false

	for _, d := range spec.Dropped {
		if d == "netns=ns1" {
			found = true
		}
	}

	if !found {
		t.Errorf("netns=ns1 not surfaced in Dropped: %v", spec.Dropped)
	}
}
