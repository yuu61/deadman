package configfile

import (
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestParseTargetPingOptions(t *testing.T) {
	input := "jumbo 192.0.2.10 size=8972 df=YES\n" +
		"ll fe80::1 probe=ssh relay=r os=JUNOS interface=ge-0/0/0.0 timeout=7.5\n" +
		"vrf 192.0.2.33 probe=ssh relay=r os=JUNOS instance=MGMT source=192.0.2.2\n"

	cfg, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	direct := params[probe.Direct](t, target(t, cfg, 0).Params)
	if direct.Echo.Size.Bytes() != 8972 || !direct.Echo.DF {
		t.Fatalf("direct = %+v", direct)
	}

	link := params[probe.SSH](t, target(t, cfg, 1).Params)
	if link.Interface != "ge-0/0/0.0" || link.Timeout.Duration() != 7500*time.Millisecond {
		t.Fatalf("link = %+v", link)
	}

	vrf := params[probe.SSH](t, target(t, cfg, 2).Params)
	if vrf.Instance != "MGMT" || vrf.Source.String() != "192.0.2.2" {
		t.Fatalf("vrf = %+v", vrf)
	}

	for i := range cfg.Lines {
		_, err = probe.Compile(target(t, cfg, i).ProbeSpec())
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
	}
}

func TestCheckRejectsInvalidPingOptions(t *testing.T) {
	for _, attrs := range []string{
		"size=huge", "size=0", "size=65508", "size=1.5", "df=maybe", "size=24 size=48", "df=",
		"probe=tcp port=443 size=8972", "probe=quic df=yes", "interface=eth0",
		"probe=ssh relay=r os=Linux instance=MGMT",
		"probe=ssh relay=r os=JUNOS timeout=0", "probe=ssh relay=r os=JUNOS timeout=-1",
		"probe=ssh relay=r os=JUNOS timeout=NaN", "probe=ssh relay=r os=JUNOS timeout=Inf",
		"probe=ssh relay=r os=JUNOS timeout=9999999999999999999999",
		"probe=ssh relay=r os=JUNOS timeout=1h", "probe=ssh relay=r os=JUNOS timeout=1h2",
	} {
		problems, err := Check(strings.NewReader("h 192.0.2.1 "+attrs), acceptSetting)
		if err != nil || len(problems) != 1 {
			t.Errorf("%s: Check = %+v, %v; want one diagnostic", attrs, problems, err)
		}
	}
}

func TestFormatPingOptions(t *testing.T) {
	input := "h 192.0.2.1 timeout=7.5 instance=MGMT size=8972 df=1 interface=ge-0/0/0.0 " +
		"os=JUNOS relay=r probe=ssh\n"

	formatted, err := Format(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	want := "h  192.0.2.1  probe=ssh relay=r os=JUNOS size=8972 df=1 " +
		"interface=ge-0/0/0.0 instance=MGMT timeout=7.5\n"
	if formatted != want {
		t.Fatalf("Format = %q, want %q", formatted, want)
	}

	again, err := Format(strings.NewReader(formatted))
	if err != nil || again != formatted {
		t.Fatalf("Format is not idempotent: %q, %v", again, err)
	}
}
