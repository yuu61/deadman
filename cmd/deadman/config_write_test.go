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

func TestParseArgsFormatWrite(t *testing.T) {
	for _, input := range [][]string{
		{"hosts.conf", "--format", "-w"},
		{"--write", "hosts.conf", "--format"},
		{"--format", "-w", "hosts.conf"},
	} {
		args, err := parseArgs(input)
		if err != nil || !args.Format || !args.Write || args.ConfigPath != "hosts.conf" {
			t.Errorf("parseArgs(%v) = %+v, %v", input, args, err)
		}
	}

	for _, input := range [][]string{
		{"hosts.conf", "-w"},
		{"--check", "hosts.conf", "--write"},
		{"--format=false", "hosts.conf", "--write"},
	} {
		_, err := parseArgs(input)
		if err == nil || !strings.Contains(err.Error(), "--write requires --format") {
			t.Errorf("parseArgs(%v): %v", input, err)
		}
	}
}

func TestProcessConfigWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"format", "h example.invalid port=443 probe=tcp\n", "h  example.invalid  probe=tcp port=443\n", false},
		{"empty", "", "", false},
		{"invalid", "host\n", "host\n", true},
		{"invalid setting", "split 0\nh 192.0.2.1\n", "split 0\nh 192.0.2.1\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.input)
			args := cliArgs{ConfigPath: path, Format: true, Write: true}

			var output bytes.Buffer

			err := processConfig(args, &output)
			if (err != nil) != tc.wantErr {
				t.Fatalf("processConfig: %v", err)
			}

			if output.Len() != 0 {
				t.Errorf("write emitted output: %q", output.String())
			}

			data := readWrittenConfig(t, path)
			if data != tc.want {
				t.Fatalf("source = %q; want %q", data, tc.want)
			}

			if !tc.wantErr {
				// Saving uses the file rather than the supplied stdout writer.
				err = processConfig(args, failedWriter{})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestProcessConfigWriteFormatFailure(t *testing.T) {
	longField := strings.Repeat("a", 32768)
	input := longField + " 192.0.2.1 probe=quic\nh " + longField + "\n"
	path := writeConfig(t, input)

	err := processConfig(cliArgs{ConfigPath: path, Format: true, Write: true}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "format configuration:") {
		t.Fatalf("expected formatting failure: %v", err)
	}

	data := readWrittenConfig(t, path)
	if data != input {
		t.Error("source changed after formatting failure")
	}
}

func TestFormatWriteCLI(t *testing.T) {
	const (
		original  = "h example.invalid port=443 probe=tcp\n"
		formatted = "h  example.invalid  probe=tcp port=443\n"
	)

	for _, tc := range []struct {
		name     string
		options  []string
		input    string
		want     string
		exit     int
		stdout   string
		problem  string
		readOnly bool
	}{
		{"short option", []string{"--format", "-w"}, original, formatted, 0, "", "", false},
		{"long option", []string{"--write", "--format"}, original, formatted, 0, "", "", false},
		{"write disabled", []string{"--format", "--write=false"}, original, original, 0, formatted, "", false},
		{
			"invalid configuration",
			[]string{"--format", "-w"},
			"host\n", "host\n", 1, "",
			":1: probe address is required", false,
		},
		{
			"read-only file",
			[]string{"--format", "-w"},
			original, original, 1, "",
			": configuration is read-only", true,
		},
		{"missing format", []string{"-w"}, original, original, 2, "", "--write requires --format", false},
		{
			"check with write",
			[]string{"--check", "--write"},
			original, original, 2, "",
			"--write requires --format", false,
		},
		{
			"conflicting modes",
			[]string{"--check", "--format", "-w"},
			original, original, 2, "",
			"--check and --format cannot be combined", false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.input)
			if tc.readOnly {
				err := os.Chmod(path, 0o400)
				if err != nil {
					t.Fatal(err)
				}
			}

			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			logDir := filepath.Join(filepath.Dir(path), "logs")
			args := append([]string{"--logging", logDir, path}, tc.options...)
			got := runConfigCLI(t, args)

			var stderr string

			switch tc.exit {
			case 0:
				stderr = ""
			case 1:
				stderr = path + tc.problem + "\n"
			case 2:
				stderr = configCLIUsage + tc.problem + "\n"
			default:
				t.Fatalf("unknown expected exit code: %d", tc.exit)
			}

			if got.exit != tc.exit || got.stdout != tc.stdout || got.stderr != stderr {
				t.Errorf(
					"CLI = %+v; want exit %d, stdout %q, stderr %q",
					got,
					tc.exit,
					tc.stdout,
					stderr,
				)
			}

			if data := readWrittenConfig(t, path); data != tc.want {
				t.Errorf("configuration = %q, want %q", data, tc.want)
			}

			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			if tc.input == tc.want &&
				(!os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime())) {
				t.Error("configuration was touched when no write was expected")
			}

			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Errorf("CLI left temporary files or created logs: %v, %v", entries, err)
			}
		})
	}
}

func TestFormatWriteCLIMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.conf")

	got := runConfigCLI(t, []string{path, "--format", "-w"})
	if got.exit != 1 || got.stdout != "" || got.stderr != missingConfigError(t, path) {
		t.Errorf("missing configuration: %+v", got)
	}

	_, err := os.Stat(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing configuration was created: %v", err)
	}
}

func readWrittenConfig(t *testing.T, path string) string {
	t.Helper()

	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}
