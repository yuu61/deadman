package probe

import "testing"

func TestParseIPSpellings(t *testing.T) {
	for raw, want := range map[string]string{
		"008.008.008.008":                   "8.8.8.8",
		"010.000.002.001":                   "10.0.2.1",
		"000.000.000.000":                   "0.0.0.0",
		"255.255.255.255":                   "255.255.255.255",
		"2001:4860:4860:0:0::8888":          "2001:4860:4860::8888",
		"2001:4860:4860:0:0:0:0:8888":       "2001:4860:4860::8888",
		"2001:0DB8:0000:0000:0001:0:0:0001": "2001:db8::1:0:0:1",
		"FE80:0:0:0:0:0:0:1%eth0":           "fe80::1%eth0",
		"::ffff:192.000.002.001":            "::ffff:192.0.2.1",
		"2001:db8::192.000.002.001":         "2001:db8::c000:201",
		"::ffff:192.000.002.001%eth0":       "::ffff:192.0.2.1%eth0",
	} {
		a, err := ParseIP(raw)
		if err != nil {
			t.Errorf("ParseIP(%q): %v", raw, err)

			continue
		}

		if got := a.String(); got != want {
			t.Errorf("ParseIP(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseIPRejectsOtherSpellings(t *testing.T) {
	for _, raw := range []string{
		"", "example.com", "1.2.3", "1.2.3.4.5", "256.0.0.1", "000256.0.0.1",
		"1..2.3", ".1.2.3", "1.2.3.", "0x08.8.8.8", "+8.8.8.8", "-8.8.8.8",
		"8.8.8.8%eth0", "8.8.8.8:80", "[::1]", "::ffff:1..2.3", "::ffff:256.0.0.1",
		"127.1", "2130706433", " 8.8.8.8", "8.8.8.8 ", "008.008.008.008.example",
	} {
		a, err := ParseIP(raw)
		if err == nil {
			t.Errorf("ParseIP(%q) = %s, want an error", raw, a)
		}
	}
}
