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
