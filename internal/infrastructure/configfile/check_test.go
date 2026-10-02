package configfile

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
)

func acceptSetting(config.Setting) error { return nil }

func TestCheckReportsPhysicalLines(t *testing.T) {
	input := "\ufeff# comment\r\n\r\n" +
		"missing\r\n" +
		"port example.com probe=tcp port=70000\r\n" +
		"unknown example.com mystery=yes\r\n" +
		"repeat example.com probe=tcp port=80 port=443\r\n" +
		"bare example.com extra words\r\n" +
		"glyph \"ascii\r\n" +
		"--- \"label\r\n" +
		"valid example.com probe=quic\r\n"
	want := []struct {
		line int
		text string
	}{
		{3, "address is required"},
		{4, "port"},
		{5, "not supported"},
		{6, "duplicate attribute"},
		{7, "bare words"},
		{8, "unterminated"},
		{9, "unterminated"},
	}

	got, err := Check(strings.NewReader(input), acceptSetting)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != len(want) {
		t.Fatalf("diagnostics = %+v, want %d", got, len(want))
	}

	for i, expected := range want {
		if got[i].Line != expected.line || !strings.Contains(got[i].Problem, expected.text) {
			t.Errorf(
				"diagnostic %d = %+v, want line %d containing %q",
				i,
				got[i],
				expected.line,
				expected.text,
			)
		}
	}
}

func TestCheckDirectiveSyntax(t *testing.T) {
	for _, input := range []string{
		"scale", "scale nope", "scale 1 extra", "split 1.5", "split",
		"glyph", "precision", "columns", "columns MIN", "columns =on", "columns MIN=maybe",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := Check(strings.NewReader(input), acceptSetting)
			if err != nil || len(got) != 1 {
				t.Fatalf("Check = %+v, %v; want one diagnostic", got, err)
			}
		})
	}
}

func TestCheckSettingsIncludingOverriddenValues(t *testing.T) {
	var got []config.Setting

	input := "Scale 0\nscale 5\nsplit 0\nprecision \"\"\nglyph ASCII\ncolumns min=YES MAX=off\n"

	diagnostics, err := Check(strings.NewReader(input), func(setting config.Setting) error {
		got = append(got, setting)

		return errors.New("frontend validation")
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []config.Setting{
		config.Scale(
			0,
		),
		config.Scale(5),
		config.Split(0),
		config.Precision(""),
		config.Glyph("ASCII"),
		config.Column{Key: "MIN", Visible: true},
		config.Column{Key: "MAX", Visible: false},
	}
	if !reflect.DeepEqual(got, want) || len(diagnostics) != len(want) {
		t.Fatalf("settings = %#v, diagnostics = %+v; want %#v", got, diagnostics, want)
	}
}

func TestCheckValidConfig(t *testing.T) {
	input := "# comment\n\n--- Servers ;# hidden\n" +
		"\"web ;# 2\" example.invalid probe=tcp port=443\n" +
		"remote 192.0.2.1 probe=ssh relay=jump.invalid os=Linux key=\"/a b\"\n" +
		"router 192.0.2.2 probe=routeros relay=192.0.2.1 username=ops password=\"a;# b\"\n" +
		"namespace 192.0.2.3 probe=netns relay=blue\n"

	got, err := Check(strings.NewReader(input), acceptSetting)
	if err != nil || len(got) != 0 {
		t.Fatalf(
			"Check = %+v, %v; want valid without resolving hosts or checking local resources",
			got,
			err,
		)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestConfigToolsReadErrors(t *testing.T) {
	_, err := Check(failedReader{}, acceptSetting)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Check read error = %v", err)
	}

	formatted, err := Format(failedReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || formatted != "" {
		t.Errorf("Format = %q, %v", formatted, err)
	}

	longLine := strings.Repeat("x", 100_000)

	_, err = Check(strings.NewReader(longLine), acceptSetting)
	if err == nil {
		t.Error("Check accepted an oversized line rejected by Parse")
	}

	formatted, err = Format(strings.NewReader("host 192.0.2.1\n" + longLine))
	if err == nil || formatted != "" {
		t.Errorf("Format returned partial output on oversized line: %q, %v", formatted, err)
	}
}
