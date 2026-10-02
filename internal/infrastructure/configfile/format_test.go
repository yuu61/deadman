package configfile

import (
	"bufio"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"blank lines", "\n \t \r\n\n", "\n\n\n"},
		{"BOM and CRLF", "\ufeff  Scale\t 5\r\nhost　 192.0.2.1", "Scale 5\nhost  192.0.2.1\n"},
		{
			"comments", "  # header  \n h  192.0.2.1  ; # keep  this\tcomment  \n",
			"# header\nh  192.0.2.1 ; # keep  this\tcomment\n",
		},
		{
			"quotes",
			"  \"my\t host\" \t192.0.2.1 probe=ssh relay=jump os=Linux key=\"/a  b;# c\"  \n",
			"\"my\t host\"  192.0.2.1  probe=ssh relay=jump os=Linux key=\"/a  b;# c\"\n",
		},
		{
			"separator",
			"  ---   Servers  /  \"Web  nodes\"  ;# hidden\n",
			"--- Servers / \"Web  nodes\" ;# hidden\n",
		},
		{"quoted hash", "\"#host\"   192.0.2.1\n", "\"#host\"  192.0.2.1\n"},
		{"empty quoted name", "\"\"   192.0.2.1\n", "\"\"  192.0.2.1\n"},
		{
			"literal single quotes", "h  192.0.2.1 probe=ssh relay=jump os=Linux user=a'b\n",
			"h  192.0.2.1  probe=ssh relay=jump os=Linux user=a'b\n",
		},
		{"Unicode before hash", "　#host 192.0.2.1\n", "　#host 192.0.2.1\n"},
		{"Unicode in marker", "--- label ;　# visible\n", "--- label ;　# visible\n"},
		{"two BOMs", "\ufeff\ufeffhost 192.0.2.1\n", "\"\"\ufeffhost  192.0.2.1\n"},
		{"literal BOM", "  \ufeffhost 192.0.2.1\n", "\"\"\ufeffhost  192.0.2.1\n"},
		{
			"target columns across sections",
			"# hosts\nscale  1\na 192.0.2.1 probe=tcp port=80\n" +
				"long-host\t2001:db8::1\tprobe=quic sni=\"a b\"\n\n" +
				"--- A very long section label\n\"web 2\" example.com ;# note\nh 192.0.2.22\n",
			"# hosts\nscale 1\na          192.0.2.1    probe=tcp port=80\n" +
				"long-host  2001:db8::1  probe=quic sni=\"a b\"\n\n" +
				"--- A very long section label\n\"web 2\"    example.com ;# note\nh          192.0.2.22\n",
		},
		{
			"display widths",
			"\"\u6771\u4eac \u62e0\u70b9\" 192.0.2.1 probe=quic\n" +
				"abc 2001:db8::1 probe=tcp port=443\ne\u0301 198.51.100.10 probe=tcp port=80\n",
			"\"\u6771\u4eac \u62e0\u70b9\"  192.0.2.1      probe=quic\n" +
				"abc          2001:db8::1    probe=tcp port=443\ne\u0301            198.51.100.10  probe=tcp port=80\n",
		},
		{
			"mid-token quotes and quoted address",
			"web\" 2\" \"example.com\" probe=ssh relay=jump os=Linux key=\"/a  b;# c\"\n" +
				"h 192.0.2.1 probe=quic\n",
			"web\" 2\"  \"example.com\"  probe=ssh relay=jump os=Linux key=\"/a  b;# c\"\n" +
				"h        192.0.2.1      probe=quic\n",
		},
		{
			"directive attribute order",
			"columns MAX=off MIN=on MAX=on\n--- port=443 probe=tcp\n",
			"columns MAX=off MIN=on MAX=on\n--- port=443 probe=tcp\n",
		},
		{
			"duplicate attributes retain diagnostics",
			"h 192.0.2.1 port=80 probe=tcp port=443\n",
			"h  192.0.2.1  port=80 probe=tcp port=443\n",
		},
		{
			"empty attributes retain diagnostics",
			"h 192.0.2.1 user= probe=ssh relay=jump os=Linux\n",
			"h  192.0.2.1  user= probe=ssh relay=jump os=Linux\n",
		},
		{
			"bare words retain parser notes",
			"h 192.0.2.1 words source=eth0 more probe=direct\n",
			"h  192.0.2.1  words source=eth0 more probe=direct\n",
		},
		{
			"duplicate keys with normalized tabs",
			"h 192.0.2.1 \"a\tb\"=1 \"a b\"=2 probe=direct\n",
			"h  192.0.2.1  \"a\tb\"=1 \"a b\"=2 probe=direct\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Format(strings.NewReader(tc.input))
			if err != nil || got != tc.want {
				t.Fatalf("Format = %q, %v; want %q", got, err, tc.want)
			}

			again, err := Format(strings.NewReader(got))
			if err != nil || again != got {
				t.Errorf("second Format = %q, %v; want %q", again, err, got)
			}

			assertSameConfig(t, tc.input, got)
		})
	}
}

func TestFormatSortsAttributes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"default direct", "resolve_family=ipv4 source=192.0.2.2", "source=192.0.2.2 resolve_family=ipv4"},
		{"direct", "resolve_family=ipv4 probe=direct source=eth0", "probe=direct source=eth0 resolve_family=ipv4"},
		{"tcp", "port=443 resolve_family=ipv4 probe=tcp", "probe=tcp resolve_family=ipv4 port=443"},
		{
			"quic", "verify=YES sni=\"example.com\" alpn=h3 port=443 resolve_family=ipv4 probe=quic",
			"probe=quic resolve_family=ipv4 port=443 alpn=h3 sni=\"example.com\" verify=YES",
		},
		{
			"ssh", "key=\"/a  b\" user=ops os=Linux resolve_family=ipv4 source=eth0 probe=ssh relay=jump",
			"probe=ssh relay=jump source=eth0 resolve_family=ipv4 os=Linux user=ops key=\"/a  b\"",
		},
		{
			"routeros",
			"password=\"a;# b=c\" verify=off username=ops scheme=https probe=routeros relay=[2001:db8::1]:8443",
			"probe=routeros relay=[2001:db8::1]:8443 scheme=https verify=off username=ops password=\"a;# b=c\"",
		},
		{
			"snmp", "community=\"a b\" relay=192.0.2.2 probe=snmp",
			"probe=snmp relay=192.0.2.2 community=\"a b\"",
		},
		{"nexthop", "source=eth0 nexthop=192.0.2.2 probe=nexthop", "probe=nexthop nexthop=192.0.2.2 source=eth0"},
		{
			"netns", "resolve_family=ipv4 source=eth0 relay=blue probe=netns",
			"probe=netns relay=blue source=eth0 resolve_family=ipv4",
		},
		{"vrf", "source=eth0 relay=blue probe=vrf", "probe=vrf relay=blue source=eth0"},
		{
			"quoted attribute keys", "\"port=443\" resolve_family=\"ipv4\" pro\"be\"=tcp",
			"pro\"be\"=tcp resolve_family=\"ipv4\" \"port=443\"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := "h 192.0.2.1 " + tc.input + " ;# keep probe=word\n"
			want := "h  192.0.2.1  " + tc.want + " ;# keep probe=word\n"

			diagnostics, err := Check(strings.NewReader(input), acceptSetting)
			if err != nil || len(diagnostics) > 0 {
				t.Fatalf("invalid input: %v, %v", diagnostics, err)
			}

			got, err := Format(strings.NewReader(input))
			if err != nil || got != want {
				t.Fatalf("Format = %q, %v; want %q", got, err, want)
			}

			assertSameConfig(t, input, got)

			again, err := Format(strings.NewReader(got))
			if err != nil || again != got {
				t.Errorf("second Format = %q, %v; want %q", again, err, got)
			}
		})
	}
}

func TestFormatRejectsOversizedAlignedLine(t *testing.T) {
	longField := strings.Repeat("a", bufio.MaxScanTokenSize/2)
	input := longField + " 192.0.2.1 probe=quic\nh " + longField + "\n"

	_, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	formatted, err := Format(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "line 1:") || formatted != "" {
		t.Fatalf(
			"Format returned unreadable or partial output: length=%d, error=%v",
			len(formatted),
			err,
		)
	}
}

func assertSameConfig(t *testing.T, before, after string) {
	t.Helper()

	original, err := Parse(strings.NewReader(before))
	if err != nil {
		t.Fatal(err)
	}

	formatted, err := Parse(strings.NewReader(after))
	if err != nil {
		t.Fatal(err)
	}

	// NaN is not equal to itself; both parses may record it for the frontend to reject.
	if math.IsNaN(original.Display.Scale) && math.IsNaN(formatted.Display.Scale) {
		original.Display.Scale = 0
		formatted.Display.Scale = 0
	}

	if !reflect.DeepEqual(original, formatted) {
		t.Errorf("formatting changed config: before=%+v after=%+v", original, formatted)
	}
}

func FuzzFormatPreservesConfig(f *testing.F) {
	for _, seed := range []string{
		"h 192.0.2.1\n", "--- ;　# label\n", "  # comment\n", "scale NaN\n",
		"\ufeff\ufeffh 192.0.2.1\n", "　#host 192.0.2.1\n", "\"quoted host\" 192.0.2.1 ;# comment\n",
		"a 192.0.2.1 probe=tcp port=443\n\"\u6771\u4eac \u62e0\u70b9\" 2001:db8::1 probe=quic\n",
		"h 192.0.2.1 port=443 resolve_family=ipv4 probe=tcp\n",
		"h 192.0.2.1 password=\"a;# b=c\" username=ops relay=jump probe=routeros\n",
		"h 192.0.2.1 \"a\tb\"=1 \"a b\"=2 probe=direct\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		formatted, err := Format(strings.NewReader(input))
		if err != nil {
			t.Skip("input exceeds the parser's line limit")
		}

		assertSameConfig(t, input, formatted)

		again, err := Format(strings.NewReader(formatted))
		if err != nil || again != formatted {
			t.Fatalf("formatting is not idempotent: %q -> %q (%v)", formatted, again, err)
		}
	})
}
