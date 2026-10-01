package configfile

import (
	"math"
	"net/netip"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

const sample = `#
#	deadman config
#
googleDNS	8.8.8.8
quad9		9.9.9.9
---
kame6		2001:2f0:0:8800::1:1

google-via-ssh	173.194.117.176 probe=ssh relay=1.2.3.4 os=Linux user=admin key=/k
snmp-t	8.8.8.8 relay=1.1.1.1 probe=snmp community=public
src-t	1.2.3.4 source=10.0.0.1
tcp-t	1.2.3.4 probe=tcp port=80
`

func TestParseSeparatorLabel(t *testing.T) {
	for _, c := range []struct {
		name, input, label string
		unterminated       bool
	}{
		{name: "plain", input: "---"},
		{name: "single dash", input: "- Routers", label: "Routers"},
		{name: "words", input: "--- Congre Routers / Switches", label: "Congre Routers / Switches"},
		{name: "whitespace", input: "  -----\t\u793e\u5185\u7db2　  ルーター  ", label: "\u793e\u5185\u7db2 ルーター"},
		{name: "quoted", input: `--- "Routers  /  Switches"`, label: "Routers  /  Switches"},
		{name: "trailer", input: "--- Routers ; # hidden", label: "Routers"},
		{name: "trailer only", input: "--- ;# hidden"},
		{name: "quoted marker", input: `--- "Group ;# 1"`, label: "Group ;# 1"},
		{name: "literal hash", input: "--- # Routers", label: "# Routers"},
		{name: "literal attribute", input: "--- probe=unknown", label: "probe=unknown"},
		{name: "open quote", input: `--- "Routers / Switches`, label: "Routers / Switches", unterminated: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(c.input + "\nhost 192.0.2.1\n"))
			if err != nil {
				t.Fatal(err)
			}

			if len(cfg.Lines) != 2 || cfg.Lines[0] != (config.Separator{Label: c.label}) {
				t.Fatalf("lines = %+v, want labeled separator followed by host", cfg.Lines)
			}

			if s := target(t, cfg, 1); s.Name != "host" || s.Addr != "192.0.2.1" {
				t.Errorf("target after separator = %+v", s)
			}

			if c.unterminated {
				if len(cfg.Notes) != 1 || !cfg.Notes[0].UnterminatedQuote ||
					len(cfg.Notes[0].Dropped) != 0 {
					t.Errorf("want only an unterminated quote warning, got %+v", cfg.Notes)
				}
			} else if len(cfg.Notes) != 0 {
				t.Errorf("separator label produced warnings: %+v", cfg.Notes)
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	cfg, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	specs := cfg.Lines

	if len(specs) != 8 {
		t.Fatalf("got %d specs, want 8: %+v", len(specs), specs)
	}

	// Comment and blank lines are dropped; tabs collapse to single spaces.
	if target(t, cfg, 0).Name != "googleDNS" || target(t, cfg, 0).Addr != "8.8.8.8" {
		t.Errorf("spec[0] = %+v", specs[0])
	}

	// Separator row.
	if specs[2] != (config.Separator{}) {
		t.Errorf("spec[2] (---) should be a separator: %+v", specs[2])
	}

	// The ssh attributes become the typed SSH parameters.
	ssh := target(t, cfg, 4)
	if ssh.Name != "google-via-ssh" || ssh.Addr != "173.194.117.176" {
		t.Errorf("ssh spec = %+v", ssh)
	}

	{
		got := params[probe.SSH](
			t,
			ssh.Params,
		)
		if got.Host != "1.2.3.4" || got.OS != probe.OSLinux || got.User != "admin" ||
			got.Key != "/k" {
			t.Errorf("ssh=%+v", got)
		}
	}

	{
		got := params[probe.SNMP](
			t,
			target(t, cfg, 5).Params,
		)
		if got.Host != "1.1.1.1" ||
			got.Community != "public" {
			t.Errorf("snmp=%+v", got)
		}
	}

	if params[probe.Direct](
		t,
		target(t, cfg, 6).Params,
	).Source != probe.SourceAddr(
		netip.MustParseAddr("10.0.0.1"),
	) {
		t.Fatal("source lost")
	}

	if params[probe.TCP](t, target(t, cfg, 7).Params).Port != probe.PortNumber(80) {
		t.Fatal("port lost")
	}
}

func TestParseConfigNexthop(t *testing.T) {
	// nexthop= becomes the typed gateway of the Nexthop parameters.
	cfg, err := Parse(strings.NewReader("gw 8.8.8.8 probe=nexthop nexthop=192.0.2.1 source=eth0\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Lines
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}

	if params[probe.Nexthop](t, target(t, cfg, 0).Params).Gateway.String() != "192.0.2.1" {
		t.Errorf(
			"nexthop = %q, want %q",
			params[probe.Nexthop](t, target(t, cfg, 0).Params).Gateway.String(),
			"192.0.2.1",
		)
	}

	// source= names an interface here, so it is a source interface, not an address.
	if params[probe.Nexthop](t, target(t, cfg, 0).Params).Source != probe.SourceInterface("eth0") {
		t.Errorf(
			"source = %v, want interface eth0",
			params[probe.Nexthop](t, target(t, cfg, 0).Params).Source,
		)
	}
}

func TestParseConfigResolveFamily(t *testing.T) {
	// resolve_family= becomes the typed family of the direct parameters; it is not
	// recorded in Dropped as an unknown attribute.
	cfg, err := Parse(strings.NewReader("web example.com resolve_family=ipv6\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Lines
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}

	if params[probe.Direct](t, target(t, cfg, 0).Params).Family != probe.FamilyIPv6 {
		t.Errorf(
			"resolve_family = %v, want %v",
			params[probe.Direct](t, target(t, cfg, 0).Params).Family,
			"ipv6",
		)
	}

	if len(cfg.Notes) != 0 {
		t.Errorf("Dropped = %v, want empty", cfg.Notes)
	}
}

func TestParseConfigQUIC(t *testing.T) {
	// The QUIC attributes (port/alpn/sni) become the typed QUIC parameters; verify= is
	// boolean, so its spelling becomes the domain's verification policy. None is recorded
	// in Dropped as unknown.
	cfg, err := Parse(
		strings.NewReader("h 1.2.3.4 probe=quic port=8443 alpn=h3 sni=x verify=on\n"),
	)
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Lines
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}

	{
		got := params[probe.QUIC](
			t,
			target(t, cfg, 0).Params,
		)
		if got.Port != probe.PortNumber(8443) || got.ALPN != "h3" || got.SNI != "x" ||
			got.Verify != probe.VerifyEnabled {
			t.Errorf("quic=%+v", got)
		}
	}

	if len(cfg.Notes) != 0 {
		t.Errorf("Dropped = %v, want empty (quic attrs must not be dropped)", cfg.Notes)
	}
}

func TestParseConfigDroppedTokens(t *testing.T) {
	// A name with spaces shifts real tokens past the address slot: this parses to
	// name="Cloudflare", address="via", with "MGMT" and "1.1.1.1" unroutable and
	// recorded in Dropped so the TUI can warn.
	cfg, err := Parse(
		strings.NewReader("Cloudflare via MGMT 1.1.1.1 probe=nexthop nexthop=10.98.38.9\n"),
	)
	if err != nil {
		t.Fatal(err)
	}

	s := target(t, cfg, 0)
	if s.Name != "Cloudflare" || s.Addr != "via" {
		t.Fatalf("name/addr = %q/%q, want Cloudflare/via", s.Name, s.Addr)
	}

	if got := strings.Join(noteOf(cfg, s.Name).Dropped, " "); got != "MGMT 1.1.1.1" {
		t.Errorf("Dropped = %q, want %q", got, "MGMT 1.1.1.1")
	}

	// The recognized attribute still becomes its typed parameter.
	if params[probe.Nexthop](t, s.Params).Gateway.String() != "10.98.38.9" {
		t.Errorf(
			"nexthop = %q, want 10.98.38.9",
			params[probe.Nexthop](t, s.Params).Gateway.String(),
		)
	}

	// A well-formed line drops nothing.
	clean, err := Parse(strings.NewReader("host 1.2.3.4 probe=nexthop nexthop=10.0.0.1\n"))
	if err != nil {
		t.Fatal(err)
	}

	if len(clean.Notes) != 0 {
		t.Errorf("clean line Dropped = %v, want empty", clean.Notes)
	}
}

func TestParseConfigCommentTrailer(t *testing.T) {
	// A ";#" trailer and everything after it is stripped — not left as stray
	// tokens (which would otherwise trip the dropped-token warning).
	cfg, err := Parse(strings.NewReader("host 1.2.3.4 ;# trailing comment\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Lines
	if len(specs) != 1 || target(t, cfg, 0).Name != "host" || target(t, cfg, 0).Addr != "1.2.3.4" {
		t.Fatalf("got %+v", specs)
	}

	if len(cfg.Notes) != 0 {
		t.Errorf("comment text leaked into Dropped: %v", cfg.Notes)
	}
}

func TestParseConfigIndentedComment(t *testing.T) {
	// An indented '#' line is a full-line comment, not a target: a host the operator
	// disabled by indenting the '#' must not be silently pinged.
	cfg, err := Parse(strings.NewReader(
		"  # disabled note\n\t#realhost 1.2.3.4\nhost 5.6.7.8\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Lines) != 1 || target(t, cfg, 0).Name != "host" ||
		target(t, cfg, 0).Addr != "5.6.7.8" {
		t.Fatalf("indented comments must be stripped, got %+v", cfg.Lines)
	}
}

func TestParseConfigDirectiveCaseInsensitive(t *testing.T) {
	// A capitalized directive applies the setting instead of becoming a phantom
	// target (the previous case-sensitive lookup silently lost both).
	cfg, err := Parse(strings.NewReader("Scale 5\nSPLIT 2\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Display.Scale != 5 {
		t.Errorf("Scale = %g, want 5", cfg.Display.Scale)
	}

	if cfg.Display.Cols != 2 {
		t.Errorf("Cols = %d, want 2", cfg.Display.Cols)
	}

	if len(cfg.Lines) != 1 || target(t, cfg, 0).Name != "host" {
		t.Fatalf("capitalized directives must not become targets, got %+v", cfg.Lines)
	}
}

func TestParseConfigSemicolonHashInValue(t *testing.T) {
	// A quoted value protects a literal ';#': it is no longer mistaken for a trailer
	// comment and truncated.
	cfg, err := Parse(strings.NewReader("host 1.2.3.4 probe=snmp relay=agent community=\"a;#b\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if got := params[probe.SNMP](t, target(t, cfg, 0).Params).Community; got != "a;#b" {
		t.Errorf("community = %q, want %q (';#' inside quotes must be preserved)", got, "a;#b")
	}

	// An unquoted ';#' is still a trailer comment (the documented way to opt out is
	// to quote the value).
	bare, err := Parse(strings.NewReader("host 1.2.3.4 probe=snmp relay=agent community=a;#b\n"))
	if err != nil {
		t.Fatal(err)
	}

	if got := params[probe.SNMP](t, target(t, bare, 0).Params).Community; got != "a" {
		t.Errorf("unquoted community = %q, want %q (trailer stripped)", got, "a")
	}
}

func TestParseConfigQuotedName(t *testing.T) {
	// Double quotes let a name contain spaces; the quotes are removed and the
	// address/attributes after the closing quote parse normally.
	cfg, err := Parse(strings.NewReader(
		"\"Cloudflare via MGMT\" 1.1.1.1 probe=nexthop nexthop=10.98.38.9\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	s := target(t, cfg, 0)
	if s.Name != "Cloudflare via MGMT" || s.Addr != "1.1.1.1" {
		t.Fatalf("name/addr = %q/%q", s.Name, s.Addr)
	}

	if params[probe.Nexthop](t, s.Params).Gateway.String() != "10.98.38.9" {
		t.Errorf(
			"nexthop = %q, want 10.98.38.9",
			params[probe.Nexthop](t, s.Params).Gateway.String(),
		)
	}

	// A quoted name must not leave stray tokens.
	if len(cfg.Notes) != 0 {
		t.Errorf("quoted name produced Dropped tokens: %v", cfg.Notes)
	}
}

func TestParseConfigUnicodeWhitespaceSeparator(t *testing.T) {
	// Fields may be separated by any Unicode whitespace, not just ASCII space/tab.
	// The ideographic space (U+3000) is routinely inserted by Japanese IMEs, so a
	// line split with it must parse into distinct name and address fields rather
	// than collapsing into one name with an empty address.
	cfg, err := Parse(strings.NewReader("host　1.2.3.4　probe=nexthop　nexthop=10.0.0.1\n"))
	if err != nil {
		t.Fatal(err)
	}

	s := target(t, cfg, 0)
	if s.Name != "host" || s.Addr != "1.2.3.4" {
		t.Fatalf("name/addr = %q/%q, want %q/%q", s.Name, s.Addr, "host", "1.2.3.4")
	}

	if params[probe.Nexthop](t, s.Params).Gateway.String() != "10.0.0.1" {
		t.Errorf("nexthop = %q, want 10.0.0.1", params[probe.Nexthop](t, s.Params).Gateway.String())
	}
}

func TestParseConfigEmptyQuotedToken(t *testing.T) {
	// Documents the chosen behavior for empty double-quote pairs. Quoting is a new
	// feature with no prior strings.Fields contract to preserve (the old parser saw
	// `""` as two literal quote characters), so this is a deliberate design choice,
	// not a regression. A standalone "" keeps its positional slot as an empty field
	// rather than vanishing — skipping it would shift `"" 1.2.3.4` to name=1.2.3.4
	// (address misread as name), which is strictly more surprising. An empty quoted
	// attribute value is still an empty value, which no attribute takes.
	cfg, err := Parse(strings.NewReader("\"\" 1.2.3.4 probe=ssh relay=jump os=Linux\n" +
		"h 1.2.3.4 probe=ssh relay=jump os=Linux key=\"\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	s := target(t, cfg, 0)
	if s.Name != "" || s.Addr != "1.2.3.4" {
		t.Fatalf("name/addr = %q/%q, want %q/%q", s.Name, s.Addr, "", "1.2.3.4")
	}

	if p := malformed(t, cfg, 1).Problem; !strings.Contains(p, `"key" needs a value`) {
		t.Errorf(`key="" problem = %q, want an empty-value problem`, p)
	}
}

func TestParseConfigUnterminatedQuote(t *testing.T) {
	// A missing closing quote absorbs the rest of the line into one field; the
	// spec is flagged so the TUI can warn instead of silently mis-binding it.
	cfg, err := Parse(
		strings.NewReader("host 1.2.3.4 probe=ssh relay=jump os=Linux user=\"admin relay=jump\n"),
	)
	if err != nil {
		t.Fatal(err)
	}

	s := target(t, cfg, 0)
	if !noteOf(cfg, s.Name).UnterminatedQuote {
		t.Errorf("expected UnterminatedQuote=true: %+v", s)
	}

	if params[probe.SSH](t, s.Params).User != "admin relay=jump" {
		t.Errorf("user = %q, want %q", params[probe.SSH](t, s.Params).User, "admin relay=jump")
	}

	// A well-formed line is not flagged.
	ok, err := Parse(
		strings.NewReader("host 1.2.3.4 probe=ssh relay=jump os=Linux user=\"admin\"\n"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(ok.Notes) != 0 {
		t.Errorf("well-formed line wrongly flagged: %+v", ok.Lines[0])
	}
}

func TestParseConfigQuotesInAttrAndLiteralSingleQuote(t *testing.T) {
	// Quoting works mid-token for an attribute value with spaces; a single quote
	// is an ordinary character.
	cfg, err := Parse(strings.NewReader(
		"host 1.2.3.4 probe=ssh relay=jump os=Linux key=\"/path with space\" user=a'b\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	s := target(t, cfg, 0)
	if params[probe.SSH](t, s.Params).Key != "/path with space" {
		t.Errorf("key = %q, want %q", params[probe.SSH](t, s.Params).Key, "/path with space")
	}

	if params[probe.SSH](t, s.Params).User != "a'b" {
		t.Errorf(
			"user = %q, want %q (single quote is literal)",
			params[probe.SSH](t, s.Params).User,
			"a'b",
		)
	}

	if len(cfg.Notes) != 0 {
		t.Errorf("unexpected Dropped: %v", cfg.Notes)
	}
}

func TestParseConfigColumns(t *testing.T) {
	cfg, err := Parse(strings.NewReader(
		"columns MIN=off MAX=off snt=on\n---\nhost 1.2.3.4\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	// The directive line is not a target; the real target still parses.
	if len(cfg.Lines) != 2 || target(t, cfg, 1).Name != "host" {
		t.Fatalf("targets = %+v", cfg.Lines)
	}

	// Only the named columns appear; keys are upper-cased; on/off both parse.
	if cfg.Display.Columns["MIN"] || cfg.Display.Columns["MAX"] || !cfg.Display.Columns["SNT"] {
		t.Errorf("columns = %+v", cfg.Display.Columns)
	}

	if _, ok := cfg.Display.Columns["RTT"]; ok {
		t.Errorf("unspecified RTT should be absent from overrides: %+v", cfg.Display.Columns)
	}
}

func TestParseConfigScaleAndPrecision(t *testing.T) {
	cfg, err := Parse(strings.NewReader(
		"scale 5\nprecision ms.1\n---\nhost 1.2.3.4\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Display.Scale != 5 {
		t.Errorf("Scale = %g, want 5", cfg.Display.Scale)
	}

	// The precision label is stored verbatim; the TUI validates it against its mode
	// table, so config keeps no duplicate list.
	if cfg.Display.Precision != "ms.1" {
		t.Errorf("Precision = %q, want ms.1", cfg.Display.Precision)
	}

	// The directive lines are not targets; the separator and real target still parse.
	if len(cfg.Lines) != 2 || target(t, cfg, 1).Name != "host" {
		t.Fatalf("targets = %+v", cfg.Lines)
	}
}

func TestParseConfigGlyph(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// The label is stored verbatim; the TUI validates it (resultbar.ParseBar plus
		// "auto"), so an unknown value is kept here and falls back to auto there.
		{"digit", "glyph digit\nhost 1.2.3.4\n", "digit"},
		{"auto", "glyph auto\nhost 1.2.3.4\n", "auto"},
		{"capitalized_keyword", "Glyph ascii\nhost 1.2.3.4\n", "ascii"},
		{"unknown_kept", "glyph bogus\nhost 1.2.3.4\n", "bogus"},
		{"no_arg", "glyph\nhost 1.2.3.4\n", ""},
		{"absent", "host 1.2.3.4\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(c.in))
			if err != nil {
				t.Fatal(err)
			}

			if cfg.Display.Glyph != c.want {
				t.Errorf("Glyph = %q, want %q", cfg.Display.Glyph, c.want)
			}

			// The directive line is not a target.
			if len(cfg.Lines) != 1 || target(t, cfg, 0).Name != "host" {
				t.Fatalf("targets = %+v", cfg.Lines)
			}
		})
	}
}

func TestParseConfigSplit(t *testing.T) {
	cfg, err := Parse(strings.NewReader("split 2\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Display.Cols != 2 {
		t.Errorf("Cols = %d, want 2", cfg.Display.Cols)
	}

	if len(cfg.Lines) != 1 || target(t, cfg, 0).Name != "host" {
		t.Fatalf("targets = %+v", cfg.Lines)
	}
}

func TestParseConfigSplitLenient(t *testing.T) {
	// A non-integer or missing split is malformed and ignored, leaving Cols unset (0) so
	// the caller falls back to the CLI/default rather than aborting the parse.
	for _, in := range []string{"split abc\n", "split 1.5\n", "split\n"} {
		cfg, err := Parse(strings.NewReader(in))
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", in, err)
		}

		if cfg.Display.Cols != 0 {
			t.Errorf("Parse(%q): Cols = %d, want 0 (unset)", in, cfg.Display.Cols)
		}
	}
}

func TestParseConfigScaleLenient(t *testing.T) {
	// A non-numeric or missing scale is malformed and ignored, leaving Scale unset (0) so
	// the caller falls back to the CLI/default rather than aborting the parse.
	for _, in := range []string{"scale abc\n", "scale 5ms\n", "scale\n"} {
		cfg, err := Parse(strings.NewReader(in))
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", in, err)
		}

		if cfg.Display.Scale != 0 {
			t.Errorf("Parse(%q): Scale = %g, want 0 (unset)", in, cfg.Display.Scale)
		}
	}
}

// A well-formed number is recorded as written even when it is no usable value (zero,
// negative, non-finite, out of range): the frontend owns the RTT-bar and column rules,
// so it validates and falls back to its default.
func TestParseConfigNumbersRecordedAsWritten(t *testing.T) {
	for _, c := range []struct {
		in    string
		scale float64
		cols  int
	}{
		{"scale -3\nsplit -2\n", -3, -2},
		{"scale 0\nsplit 0\n", 0, 0},
		{"scale 1e300\n", 1e300, 0},
	} {
		cfg, err := Parse(strings.NewReader(c.in))
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", c.in, err)
		}

		if cfg.Display.Scale != c.scale || cfg.Display.Cols != c.cols {
			t.Errorf("Parse(%q) = scale %g split %d; want %g, %d",
				c.in, cfg.Display.Scale, cfg.Display.Cols, c.scale, c.cols)
		}
	}

	cfg, err := Parse(strings.NewReader("scale nan\n"))
	if err != nil || !math.IsNaN(cfg.Display.Scale) {
		t.Errorf(
			"scale nan = %g, %v; want NaN recorded for the frontend to reject",
			cfg.Display.Scale,
			err,
		)
	}
}

// The last well-formed line of a directive wins; a malformed later line is ignored.
func TestParseConfigLastDirectiveWins(t *testing.T) {
	cfg, err := Parse(strings.NewReader(
		"scale 5\nscale 2\nscale bogus\nsplit 3\nsplit 2\nglyph block\nglyph digit\n" +
			"columns MIN=off\ncolumns MIN=on MAX=off\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	d := cfg.Display
	if d.Scale != 2 || d.Cols != 2 || d.Glyph != "digit" || !d.Columns["MIN"] || d.Columns["MAX"] {
		t.Errorf("display = %+v, want the last well-formed value of each directive", d)
	}
}

// A directive written without its value is ignored, whichever directive it is: the
// display keeps its defaults and the line does not become a target.
func TestParseConfigDirectiveWithoutValue(t *testing.T) {
	for keyword := range directives {
		t.Run(keyword, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(keyword + "\nhost 1.2.3.4\n"))
			if err != nil {
				t.Fatal(err)
			}

			d := cfg.Display
			if d.Scale != 0 || d.Precision != "" || d.Glyph != "" || d.Cols != 0 ||
				len(d.Columns) != 0 {
				t.Errorf("display = %+v, want the defaults", d)
			}

			if len(cfg.Lines) != 1 || target(t, cfg, 0).Name != "host" {
				t.Fatalf("targets = %+v", cfg.Lines)
			}
		})
	}
}

func TestParseConfigScaleFractional(t *testing.T) {
	// A fractional scale is accepted (sub-ms resolution); the value is stored verbatim
	// as a float so the result bar can bucket sub-millisecond RTTs.
	cfg, err := Parse(strings.NewReader("scale 0.5\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Display.Scale != 0.5 {
		t.Errorf("Scale = %g, want 0.5", cfg.Display.Scale)
	}
}

func TestParseConfigEmpty(t *testing.T) {
	cfg, err := Parse(strings.NewReader("\n  \n#only comments\n"))
	if err != nil {
		t.Fatal(err)
	}

	specs := cfg.Lines
	if len(specs) != 0 {
		t.Fatalf("got %d specs, want 0: %+v", len(specs), specs)
	}
}

// parseBool is the config file's one boolean vocabulary, shared by the "columns"
// directive and the boolean attributes: four spellings each way, case-insensitive, and
// anything else (including "") reported as unknown so the caller keeps its own default.
func TestParseBool(t *testing.T) {
	cases := []struct {
		in       string
		want, ok bool
	}{
		{"on", true, true},
		{"TRUE", true, true},
		{"Yes", true, true},
		{"1", true, true},
		{"off", false, true},
		{"False", false, true},
		{"NO", false, true},
		{"0", false, true},
		{"", false, false},
		{"enable", false, false},
		{"2", false, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseBool(c.in)
			if got != c.want || (err == nil) != c.ok {
				t.Errorf("parseBool(%q) = %v, %v; want %v, ok=%v", c.in, got, err, c.want, c.ok)
			}
		})
	}
}

// A boolean attribute is stored in the domain's canonical form, so the probing modes
// read its meaning (probe.Spec.Verify) without knowing the file's spellings; an
// unrecognized spelling is kept as written, which leaves the mode's default.
func TestParseConfigBoolAttrCanonical(t *testing.T) {
	for attr, want := range map[string]probe.Verification{
		"on":  probe.VerifyEnabled,
		"YES": probe.VerifyEnabled,
		"1":   probe.VerifyEnabled,
		"off": probe.VerifyDisabled,
		"No":  probe.VerifyDisabled,
	} {
		cfg, err := Parse(strings.NewReader("h 192.0.2.1 probe=quic verify=" + attr))
		if err != nil {
			t.Fatal(err)
		}

		if got := params[probe.QUIC](t, target(t, cfg, 0).Params).Verify; got != want {
			t.Errorf("%s: %v", attr, got)
		}
	}
}

func params[P probe.Params](t *testing.T, p probe.Params) P {
	t.Helper()

	v, ok := p.(P)
	if !ok {
		t.Fatalf("unexpected params: %T", p)
	}

	return v
}

// target returns line i of cfg as a target line, failing the test when it is another kind.
func target(t *testing.T, cfg config.Config, i int) config.Target {
	t.Helper()

	if i >= len(cfg.Lines) {
		t.Fatalf("line %d missing: %+v", i, cfg.Lines)
	}

	l, ok := cfg.Lines[i].(config.Target)
	if !ok {
		t.Fatalf("line %d = %+v, want a target line", i, cfg.Lines[i])
	}

	return l
}

// malformed returns line i of cfg as a malformed line, failing the test when it is
// another kind.
func malformed(t *testing.T, cfg config.Config, i int) config.Malformed {
	t.Helper()

	if i >= len(cfg.Lines) {
		t.Fatalf("line %d missing: %+v", i, cfg.Lines)
	}

	l, ok := cfg.Lines[i].(config.Malformed)
	if !ok {
		t.Fatalf("line %d = %+v, want a malformed line", i, cfg.Lines[i])
	}

	return l
}

// noteOf returns cfg's note on the line named name; the zero Note when the parser noted
// nothing there.
func noteOf(cfg config.Config, name string) config.Note {
	for _, n := range cfg.Notes {
		if n.Name == name {
			return n
		}
	}

	return config.Note{}
}

// A UTF-8 BOM (U+FEFF) on the first line — routinely emitted by Windows editors — must
// be stripped, or it sticks to the first token: a "scale" directive becomes a phantom
// target and a host's name is garbled.
func TestParseConfigStripsBOM(t *testing.T) {
	cfg, err := Parse(strings.NewReader("\ufeffscale 5\nhost 1.2.3.4\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Display.Scale != 5 {
		t.Errorf(
			"Scale = %g, want 5 (BOM must not block the first-line directive)",
			cfg.Display.Scale,
		)
	}

	if len(cfg.Lines) != 1 {
		t.Fatalf("got %d targets, want 1 (no phantom from the BOM line)", len(cfg.Lines))
	}

	if got := target(t, cfg, 0); got.Name != "host" || got.Addr != "1.2.3.4" {
		t.Errorf("target = {Name:%q Addr:%q}, want host/1.2.3.4", got.Name, got.Addr)
	}
}
