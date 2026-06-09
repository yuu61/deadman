package config

import (
	"strings"
	"testing"
)

const sample = `#
#	deadman config
#
googleDNS	8.8.8.8
quad9		9.9.9.9
---
kame6		2001:2f0:0:8800::1:1

google-via-ssh	173.194.117.176 relay=1.2.3.4 os=Linux user=admin key=/k
snmp-t	8.8.8.8 relay=1.1.1.1 via=snmp community=public
src-t	1.2.3.4 source=10.0.0.1
tcp-t	1.2.3.4 tcp=dstport:80
`

func TestParseConfig(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}

	specs := cfg.Targets

	if len(specs) != 8 {
		t.Fatalf("got %d specs, want 8: %+v", len(specs), specs)
	}

	// Comment and blank lines are dropped; tabs collapse to single spaces.
	if specs[0].Name != "googleDNS" || specs[0].Addr != "8.8.8.8" {
		t.Errorf("spec[0] = %+v", specs[0])
	}

	if specs[0].IsSeparator {
		t.Error("spec[0] should not be a separator")
	}

	// Separator row.
	if !specs[2].IsSeparator {
		t.Errorf("spec[2] (---) should be a separator: %+v", specs[2])
	}

	// SSH relay attributes land in the relay map.
	ssh := specs[4]
	if ssh.Name != "google-via-ssh" || ssh.Addr != "173.194.117.176" {
		t.Errorf("ssh spec = %+v", ssh)
	}

	for k, want := range map[string]string{"relay": "1.2.3.4", "os": "Linux", "user": "admin", "key": "/k"} {
		if ssh.Relay[k] != want {
			t.Errorf("ssh.Relay[%q] = %q, want %q", k, ssh.Relay[k], want)
		}
	}

	// SNMP via.
	snmp := specs[5]
	if snmp.Relay["via"] != "snmp" || snmp.Relay["community"] != "public" ||
		snmp.Relay["relay"] != "1.1.1.1" {
		t.Errorf("snmp spec = %+v", snmp)
	}

	// source and tcp are stored on their own fields, not the relay map.
	if specs[6].Source != "10.0.0.1" {
		t.Errorf("source spec = %+v", specs[6])
	}

	if specs[7].TCP != "dstport:80" {
		t.Errorf("tcp spec = %+v", specs[7])
	}
}

func TestParseConfigNexthop(t *testing.T) {
	// nexthop is a relay key, so it lands in the relay map (like via/relay).
	cfg, err := ParseConfig(strings.NewReader("gw 8.8.8.8 nexthop=192.0.2.1 source=eth0\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Targets
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}

	if specs[0].Relay["nexthop"] != "192.0.2.1" {
		t.Errorf("nexthop = %q, want %q", specs[0].Relay["nexthop"], "192.0.2.1")
	}

	// source still lands on its own field, not the relay map.
	if specs[0].Source != "eth0" {
		t.Errorf("source = %q, want %q", specs[0].Source, "eth0")
	}
}

func TestParseConfigDroppedTokens(t *testing.T) {
	// A name with spaces shifts real tokens past the address slot: this parses to
	// name="Cloudflare", address="via", with "MGMT" and "1.1.1.1" unroutable and
	// recorded in Dropped so the TUI can warn.
	cfg, err := ParseConfig(strings.NewReader("Cloudflare via MGMT 1.1.1.1 nexthop=10.98.38.9\n"))
	if err != nil {
		t.Fatal(err)
	}

	s := cfg.Targets[0]
	if s.Name != "Cloudflare" || s.Addr != "via" {
		t.Fatalf("name/addr = %q/%q, want Cloudflare/via", s.Name, s.Addr)
	}

	if got := strings.Join(s.Dropped, " "); got != "MGMT 1.1.1.1" {
		t.Errorf("Dropped = %q, want %q", got, "MGMT 1.1.1.1")
	}

	// The recognized attribute still lands in the relay map.
	if s.Relay["nexthop"] != "10.98.38.9" {
		t.Errorf("nexthop = %q, want 10.98.38.9", s.Relay["nexthop"])
	}

	// A well-formed line drops nothing.
	clean, _ := ParseConfig(strings.NewReader("host 1.2.3.4 nexthop=10.0.0.1\n"))
	if len(clean.Targets[0].Dropped) != 0 {
		t.Errorf("clean line Dropped = %v, want empty", clean.Targets[0].Dropped)
	}
}

func TestParseConfigCommentTrailer(t *testing.T) {
	// A ";#" trailer and everything after it is stripped — not left as stray
	// tokens (which would otherwise trip the dropped-token warning).
	cfg, err := ParseConfig(strings.NewReader("host 1.2.3.4 ;# trailing comment\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Targets
	if len(specs) != 1 || specs[0].Name != "host" || specs[0].Addr != "1.2.3.4" {
		t.Fatalf("got %+v", specs)
	}

	if len(specs[0].Dropped) != 0 {
		t.Errorf("comment text leaked into Dropped: %v", specs[0].Dropped)
	}
}

func TestParseConfigQuotedName(t *testing.T) {
	// Double quotes let a name contain spaces; the quotes are removed and the
	// address/attributes after the closing quote parse normally.
	cfg, err := ParseConfig(strings.NewReader(
		"\"Cloudflare via MGMT\" 1.1.1.1 nexthop=10.98.38.9\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	s := cfg.Targets[0]
	if s.Name != "Cloudflare via MGMT" || s.Addr != "1.1.1.1" {
		t.Fatalf("name/addr = %q/%q", s.Name, s.Addr)
	}

	if s.Relay["nexthop"] != "10.98.38.9" {
		t.Errorf("nexthop = %q, want 10.98.38.9", s.Relay["nexthop"])
	}

	// A quoted name must not leave stray tokens.
	if len(s.Dropped) != 0 {
		t.Errorf("quoted name produced Dropped tokens: %v", s.Dropped)
	}
}

func TestParseConfigUnterminatedQuote(t *testing.T) {
	// A missing closing quote absorbs the rest of the line into one field; the
	// spec is flagged so the TUI can warn instead of silently mis-binding it.
	cfg, err := ParseConfig(strings.NewReader("host 1.2.3.4 user=\"admin relay=jump\n"))
	if err != nil {
		t.Fatal(err)
	}

	s := cfg.Targets[0]
	if !s.UnterminatedQuote {
		t.Errorf("expected UnterminatedQuote=true: %+v", s)
	}

	if s.Relay["user"] != "admin relay=jump" {
		t.Errorf("user = %q, want %q", s.Relay["user"], "admin relay=jump")
	}

	// A well-formed line is not flagged.
	ok, _ := ParseConfig(strings.NewReader("host 1.2.3.4 user=\"admin\"\n"))
	if ok.Targets[0].UnterminatedQuote {
		t.Errorf("well-formed line wrongly flagged: %+v", ok.Targets[0])
	}
}

func TestParseConfigQuotesInAttrAndLiteralSingleQuote(t *testing.T) {
	// Quoting works mid-token for an attribute value with spaces; a single quote
	// is an ordinary character.
	cfg, err := ParseConfig(strings.NewReader(
		"host 1.2.3.4 key=\"/path with space\" user=a'b\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	s := cfg.Targets[0]
	if s.Relay["key"] != "/path with space" {
		t.Errorf("key = %q, want %q", s.Relay["key"], "/path with space")
	}

	if s.Relay["user"] != "a'b" {
		t.Errorf("user = %q, want %q (single quote is literal)", s.Relay["user"], "a'b")
	}

	if len(s.Dropped) != 0 {
		t.Errorf("unexpected Dropped: %v", s.Dropped)
	}
}

func TestParseConfigColumns(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader(
		"columns MIN=off MAX=off snt=on\n---\nhost 1.2.3.4\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	// The directive line is not a target; the real target still parses.
	if len(cfg.Targets) != 2 || cfg.Targets[1].Name != "host" {
		t.Fatalf("targets = %+v", cfg.Targets)
	}

	// Only the named columns appear; keys are upper-cased; on/off both parse.
	if cfg.Columns["MIN"] || cfg.Columns["MAX"] || !cfg.Columns["SNT"] {
		t.Errorf("columns = %+v", cfg.Columns)
	}

	if _, ok := cfg.Columns["RTT"]; ok {
		t.Errorf("unspecified RTT should be absent from overrides: %+v", cfg.Columns)
	}
}

func TestParseConfigEmpty(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader("\n  \n#only comments\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Targets
	if len(specs) != 0 {
		t.Fatalf("got %d specs, want 0: %+v", len(specs), specs)
	}
}
