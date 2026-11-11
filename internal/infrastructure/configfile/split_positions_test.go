package configfile

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
)

func TestColumnBreakConfigRoundTrip(t *testing.T) {
	input := "\ufeff\t#PaNe\t\u4f1a\u5834 LAN / Switches  \r\n" +
		"local\t192.0.2.1\n--- Routers\n#blank\n" +
		" #pane\u3000\u5916\u90e8\u63a5\u7d9a ;# \u898b\u51fa\u3057\u306e\u4e00\u90e8\nremote 198.51.100.1\n" +
		"#columns ordinary comment\n\"#pane\" 203.0.113.1\n"

	cfg, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	for index, label := range map[int]string{
		0: "\u4f1a\u5834 LAN / Switches",
		4: "\u5916\u90e8\u63a5\u7d9a ;# \u898b\u51fa\u3057\u306e\u4e00\u90e8",
	} {
		if cfg.Lines[index] != (config.ColumnBreak{Label: label}) {
			t.Errorf("line %d = %+v, want heading %q", index, cfg.Lines[index], label)
		}
	}

	got := target(t, cfg, 6)
	if got.Name != "#pane" || len(cfg.Lines) != 7 || len(cfg.Notes) != 0 {
		t.Fatalf("unexpected targets or parser notes: %+v", cfg)
	}

	diagnostics, err := Check(strings.NewReader(input), acceptSetting, acceptPlan)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("Check = %+v, %v", diagnostics, err)
	}

	formatted, err := Format(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	after, err := Parse(strings.NewReader(formatted))
	if err != nil || !reflect.DeepEqual(cfg, after) {
		t.Fatalf("format changed split configuration: %+v, %v\n%s", after, err, formatted)
	}

	again, err := Format(strings.NewReader(formatted))
	if err != nil || again != formatted {
		t.Fatalf("format is not idempotent: %q, %v", again, err)
	}
}

func TestColumnBreakDescriptionOptional(t *testing.T) {
	input := "# comment\n\n#pane\n#pane LAN\n #PANE\t \n"

	diagnostics, err := Check(
		strings.NewReader(input),
		acceptSetting,
		acceptPlan,
	)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("Check = %+v, %v", diagnostics, err)
	}

	cfg, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	want := []config.Line{
		config.ColumnBreak{},
		config.ColumnBreak{Label: "LAN"},
		config.ColumnBreak{},
	}
	if !reflect.DeepEqual(cfg.Lines, want) {
		t.Errorf("lines = %+v, want %+v", cfg.Lines, want)
	}

	formatted, err := Format(strings.NewReader(input))
	if err != nil || strings.Contains(formatted, "#pane \n") {
		t.Errorf("format left trailing spaces on a bare break: %q, %v", formatted, err)
	}
}

func TestCommentedSplitDirectiveIsNotColumnBreak(t *testing.T) {
	cfg, err := Parse(strings.NewReader("#split 2\n#columns ADDRESS=off\nhost 192.0.2.1\n"))
	if err != nil || len(cfg.Lines) != 1 {
		t.Fatalf("commented display settings changed the layout: %+v, %v", cfg, err)
	}
}
