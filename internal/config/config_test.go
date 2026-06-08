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
	specs, err := ParseConfig(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}

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

func TestParseConfigCommentTrailer(t *testing.T) {
	// A ";#" trailer is stripped.
	specs, err := ParseConfig(strings.NewReader("host 1.2.3.4 ;# trailing comment\n"))
	if err != nil {
		t.Fatal(err)
	}

	if len(specs) != 1 || specs[0].Name != "host" || specs[0].Addr != "1.2.3.4" {
		t.Fatalf("got %+v", specs)
	}
}

func TestParseConfigEmpty(t *testing.T) {
	specs, err := ParseConfig(strings.NewReader("\n  \n#only comments\n"))
	if err != nil {
		t.Fatal(err)
	}

	if len(specs) != 0 {
		t.Fatalf("got %d specs, want 0: %+v", len(specs), specs)
	}
}
