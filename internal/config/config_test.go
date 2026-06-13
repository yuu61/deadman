package config

import (
	"math"
	"strings"
	"testing"
)

// TestValidScale pins the usable-scale window: the [minScale, maxScale] bounds (and the
// implicit rejection of zero, negatives, NaN and ±Inf). Extreme finite values are
// rejected too — below minScale the footer label balloons and every bar overflows.
func TestValidScale(t *testing.T) {
	cases := []struct {
		v  float64
		ok bool
	}{
		{10, true},
		{0.01, true},     // sub-ms ladder bottom.
		{minScale, true}, // inclusive lower bound.
		{maxScale, true}, // inclusive upper bound.
		{0, false},
		{-5, false},
		{1e-300, false}, // far below minScale: ~300-char footer label, bar all █.
		{1e300, false},  // far above maxScale.
		{math.Inf(1), false},
		{math.Inf(-1), false},
		{math.NaN(), false},
	}
	for _, c := range cases {
		if got := ValidScale(c.v); got != c.ok {
			t.Errorf("ValidScale(%g) = %v, want %v", c.v, got, c.ok)
		}
	}
}

// TestScaleOrDefault confirms the shared "invalid → default" normalization: a usable
// value passes through, anything ValidScale rejects becomes DefaultScale.
func TestScaleOrDefault(t *testing.T) {
	cases := []struct {
		v, want float64
	}{
		{5, 5},
		{0.5, 0.5},
		{0, DefaultScale},
		{-1, DefaultScale},
		{1e-300, DefaultScale},
		{math.Inf(1), DefaultScale},
		{math.NaN(), DefaultScale},
	}
	for _, c := range cases {
		if got := ScaleOrDefault(c.v); got != c.want {
			t.Errorf("ScaleOrDefault(%g) = %g, want %g", c.v, got, c.want)
		}
	}
}

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

func TestParseConfigResolveFamily(t *testing.T) {
	// resolve_family is a relay key, so it lands in the relay map verbatim (the ping
	// layer normalizes it); it is not recorded in Dropped as an unknown attribute.
	cfg, err := ParseConfig(strings.NewReader("web example.com resolve_family=ipv6\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Targets
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}

	if specs[0].Relay["resolve_family"] != "ipv6" {
		t.Errorf("resolve_family = %q, want %q", specs[0].Relay["resolve_family"], "ipv6")
	}

	if len(specs[0].Dropped) != 0 {
		t.Errorf("Dropped = %v, want empty", specs[0].Dropped)
	}
}

func TestParseConfigQUIC(t *testing.T) {
	// The QUIC sub-attributes (port/alpn/sni) are relay keys, so they land in the
	// relay map verbatim alongside via/verify; none is recorded in Dropped as unknown.
	cfg, err := ParseConfig(
		strings.NewReader("h 1.2.3.4 via=quic port=8443 alpn=h3 sni=x verify=on\n"),
	)
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Targets
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}

	for k, want := range map[string]string{
		"via": "quic", "port": "8443", "alpn": "h3", "sni": "x", "verify": "on",
	} {
		if specs[0].Relay[k] != want {
			t.Errorf("Relay[%q] = %q, want %q", k, specs[0].Relay[k], want)
		}
	}

	if len(specs[0].Dropped) != 0 {
		t.Errorf("Dropped = %v, want empty (quic attrs must not be dropped)", specs[0].Dropped)
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

func TestParseConfigIndentedComment(t *testing.T) {
	// An indented '#' line is a full-line comment, not a target: a host the operator
	// disabled by indenting the '#' must not be silently pinged.
	cfg, err := ParseConfig(strings.NewReader(
		"  # disabled note\n\t#realhost 1.2.3.4\nhost 5.6.7.8\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "host" || cfg.Targets[0].Addr != "5.6.7.8" {
		t.Fatalf("indented comments must be stripped, got %+v", cfg.Targets)
	}
}

func TestParseConfigDirectiveCaseInsensitive(t *testing.T) {
	// A capitalized directive applies the setting instead of becoming a phantom
	// target (the previous case-sensitive lookup silently lost both).
	cfg, err := ParseConfig(strings.NewReader("Scale 5\nSPLIT 2\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Scale != 5 {
		t.Errorf("Scale = %g, want 5", cfg.Scale)
	}

	if cfg.Cols != 2 {
		t.Errorf("Cols = %d, want 2", cfg.Cols)
	}

	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "host" {
		t.Fatalf("capitalized directives must not become targets, got %+v", cfg.Targets)
	}
}

func TestParseConfigSemicolonHashInValue(t *testing.T) {
	// A quoted value protects a literal ';#': it is no longer mistaken for a trailer
	// comment and truncated.
	cfg, err := ParseConfig(strings.NewReader("host 1.2.3.4 community=\"a;#b\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Targets[0].Relay["community"]; got != "a;#b" {
		t.Errorf("community = %q, want %q (';#' inside quotes must be preserved)", got, "a;#b")
	}

	// An unquoted ';#' is still a trailer comment (the documented way to opt out is
	// to quote the value).
	bare, err := ParseConfig(strings.NewReader("host 1.2.3.4 community=a;#b\n"))
	if err != nil {
		t.Fatal(err)
	}

	if got := bare.Targets[0].Relay["community"]; got != "a" {
		t.Errorf("unquoted community = %q, want %q (trailer stripped)", got, "a")
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

func TestParseConfigUnicodeWhitespaceSeparator(t *testing.T) {
	// Fields may be separated by any Unicode whitespace, not just ASCII space/tab.
	// The ideographic space (U+3000) is routinely inserted by Japanese IMEs, so a
	// line split with it must parse into distinct name and address fields rather
	// than collapsing into one name with an empty address.
	cfg, err := ParseConfig(strings.NewReader("host　1.2.3.4　nexthop=10.0.0.1\n"))
	if err != nil {
		t.Fatal(err)
	}

	s := cfg.Targets[0]
	if s.Name != "host" || s.Addr != "1.2.3.4" {
		t.Fatalf("name/addr = %q/%q, want %q/%q", s.Name, s.Addr, "host", "1.2.3.4")
	}

	if s.Relay["nexthop"] != "10.0.0.1" {
		t.Errorf("nexthop = %q, want 10.0.0.1", s.Relay["nexthop"])
	}
}

func TestParseConfigEmptyQuotedToken(t *testing.T) {
	// Documents the chosen behavior for empty double-quote pairs. Quoting is a new
	// feature with no prior strings.Fields contract to preserve (the old parser saw
	// `""` as two literal quote characters), so this is a deliberate design choice,
	// not a regression. A standalone "" keeps its positional slot as an empty field
	// rather than vanishing — skipping it would shift `"" 1.2.3.4` to name=1.2.3.4
	// (address misread as name), which is strictly more surprising.
	cfg, err := ParseConfig(strings.NewReader("\"\" 1.2.3.4 key=\"\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	s := cfg.Targets[0]
	if s.Name != "" || s.Addr != "1.2.3.4" {
		t.Fatalf("name/addr = %q/%q, want %q/%q", s.Name, s.Addr, "", "1.2.3.4")
	}

	// key="" is an explicit empty value: the key must be present in the relay map,
	// not absent. Comma-ok distinguishes present-but-empty from missing.
	if v, ok := s.Relay["key"]; !ok || v != "" {
		t.Errorf(`Relay["key"] = %q, present=%v; want present empty value`, v, ok)
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

func TestParseConfigScaleAndPrecision(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader(
		"scale 5\nprecision ms.1\n---\nhost 1.2.3.4\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Scale != 5 {
		t.Errorf("Scale = %g, want 5", cfg.Scale)
	}

	// The precision label is stored verbatim; the TUI validates it against its mode
	// table, so config keeps no duplicate list.
	if cfg.Precision != "ms.1" {
		t.Errorf("Precision = %q, want ms.1", cfg.Precision)
	}

	// The directive lines are not targets; the separator and real target still parse.
	if len(cfg.Targets) != 2 || cfg.Targets[1].Name != "host" {
		t.Fatalf("targets = %+v", cfg.Targets)
	}
}

func TestParseConfigSplit(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader("split 2\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Cols != 2 {
		t.Errorf("Cols = %d, want 2", cfg.Cols)
	}

	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "host" {
		t.Fatalf("targets = %+v", cfg.Targets)
	}
}

func TestParseConfigSplitLenient(t *testing.T) {
	// A non-numeric or non-positive split is ignored, leaving Cols unset (0) so the
	// caller falls back to the CLI/default rather than aborting the parse.
	for _, in := range []string{"split abc\n", "split -2\n", "split 0\n", "split\n"} {
		cfg, err := ParseConfig(strings.NewReader(in))
		if err != nil {
			t.Fatalf("ParseConfig(%q) error: %v", in, err)
		}

		if cfg.Cols != 0 {
			t.Errorf("ParseConfig(%q): Cols = %d, want 0 (unset)", in, cfg.Cols)
		}
	}
}

func TestParseConfigScaleLenient(t *testing.T) {
	// A non-numeric, non-positive, or non-finite scale is ignored, leaving Scale unset
	// (0) so the caller falls back to the CLI/default rather than aborting the parse.
	// "scale inf" is the case the math.IsInf guard catches (ParseFloat accepts it and
	// +Inf > 0); "scale nan" is caught by the n > 0 check (NaN > 0 is false).
	for _, in := range []string{
		"scale abc\n", "scale -3\n", "scale 0\n", "scale\n", "scale inf\n", "scale nan\n",
	} {
		cfg, err := ParseConfig(strings.NewReader(in))
		if err != nil {
			t.Fatalf("ParseConfig(%q) error: %v", in, err)
		}

		if cfg.Scale != 0 {
			t.Errorf("ParseConfig(%q): Scale = %g, want 0 (unset)", in, cfg.Scale)
		}
	}
}

func TestParseConfigScaleFractional(t *testing.T) {
	// A fractional scale is accepted (sub-ms resolution); the value is stored verbatim
	// as a float so the result bar can bucket sub-millisecond RTTs.
	cfg, err := ParseConfig(strings.NewReader("scale 0.5\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Scale != 0.5 {
		t.Errorf("Scale = %g, want 0.5", cfg.Scale)
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
