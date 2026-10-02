package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgsConfigTools(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		check  bool
		format bool
	}{
		{[]string{"--check", "hosts.conf"}, true, false},
		{[]string{"hosts.conf", "--check"}, true, false},
		{[]string{"--format", "hosts.conf"}, false, true},
		{[]string{"hosts.conf", "--format"}, false, true},
		{[]string{"hosts.conf"}, false, false},
	} {
		args, err := parseArgs(tc.args)
		if err != nil || args.Check != tc.check || args.Format != tc.format ||
			args.ConfigPath != "hosts.conf" {
			t.Errorf("parseArgs(%v) = %+v, %v", tc.args, args, err)
		}
	}

	for _, args := range [][]string{
		{"--check"}, {"--format"}, {"--check", "hosts.conf", "--format"},
	} {
		_, err := parseArgs(args)
		if err == nil {
			t.Errorf("parseArgs(%v) should fail", args)
		}
	}
}

func TestProcessConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		format bool
		want   string
	}{
		{"check", "web example.invalid probe=tcp port=443\n", false, ""},
		{
			"format", "# header\n web\t example.invalid   probe=tcp port=443 ;# note\r\n", true,
			"# header\nweb example.invalid probe=tcp port=443 ;# note\n",
		},
		{"empty check", "", false, ""},
		{"empty format", "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.input)
			logDir := filepath.Join(t.TempDir(), "logs")

			var output bytes.Buffer

			err := processConfig(
				cliArgs{ConfigPath: path, Check: !tc.format, Format: tc.format, LogDir: logDir},
				&output,
			)
			if err != nil {
				t.Fatal(err)
			}

			want := tc.want
			if !tc.format {
				want = path + ": OK\n"
			}

			if output.String() != want {
				t.Errorf("output = %q, want %q", output.String(), want)
			}

			root, err := os.OpenRoot(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()

			original, err := root.ReadFile(filepath.Base(path))
			if err != nil || string(original) != tc.input {
				t.Errorf("source changed: %q, %v", original, err)
			}

			_, err = os.Stat(logDir)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("log directory was created: %v", err)
			}
		})
	}
}

func TestProcessConfigRejectsInvalidSettings(t *testing.T) {
	input := "# heading\n" +
		"h example.com probe=tcp\nscale NaN\nsplit 0\nprecision typo\nglyph bogus\n" +
		"columns RESULT=off MIN=maybe\nscale 1 extra\nscale\n" +
		"h example.com extra\n--- \"open\n"
	path := writeConfig(t, input)

	for _, format := range []bool{false, true} {
		var output bytes.Buffer

		err := processConfig(cliArgs{ConfigPath: path, Check: !format, Format: format}, &output)
		if err == nil {
			t.Fatal("expected invalid config to fail")
		}

		for _, suffix := range []string{
			":2: ", ":3: ", ":4: ", ":5: ", ":6: ", ":7: ", ":8: ", ":9: ", ":10: ", ":11: ",
		} {
			if !strings.Contains(err.Error(), path+suffix) {
				t.Errorf("missing diagnostic for %s: %v", suffix, err)
			}
		}

		if output.Len() != 0 {
			t.Errorf("invalid config emitted output: %q", output.String())
		}
	}
}

func TestProcessConfigIOErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.conf")

	err := processConfig(cliArgs{ConfigPath: path, Check: true}, io.Discard)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}

	path = writeConfig(t, "h 192.0.2.1\n")
	for _, format := range []bool{false, true} {
		err = processConfig(
			cliArgs{ConfigPath: path, Check: !format, Format: format},
			failedWriter{},
		)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("write error: %v", err)
		}
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
