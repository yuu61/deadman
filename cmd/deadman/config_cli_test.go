package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	configCLIProcessEnv = "DEADMAN_TEST_CONFIG_CLI"
	configCLIUsage      = "usage: deadman [options] configfile\n"
)

type configCLIResult struct {
	stdout string
	stderr string
	exit   int
}

func TestMain(tests *testing.M) {
	if os.Getenv(configCLIProcessEnv) == "1" {
		// Reuse the test binary to exercise main without the test runner's output.
		// The parent places CLI arguments after a "--" delimiter.
		os.Args = append(os.Args[:1], os.Args[2:]...)

		main()

		return
	}

	tests.Run()
}

func TestConfigToolsCLI(t *testing.T) {
	valid := writeConfig(t, "h example.invalid port=443 probe=tcp\n")
	invalid := writeConfig(t, "host\n")
	missing := filepath.Join(t.TempDir(), "missing.conf")

	for _, tc := range []struct {
		name   string
		args   []string
		exit   int
		stdout string
		stderr string
	}{
		{"check", []string{"--check", valid}, 0, valid + ": OK\n", ""},
		{"format", []string{"--format", valid}, 0, "h  example.invalid  probe=tcp port=443\n", ""},
		{"invalid check", []string{"--check", invalid}, 1, "", invalid + ":1: probe address is required\n"},
		{"invalid format", []string{"--format", invalid}, 1, "", invalid + ":1: probe address is required\n"},
		{
			"missing check",
			[]string{"--check", missing},
			1, "",
			missingConfigError(t, missing),
		},
		{
			"missing format",
			[]string{"--format", missing},
			1, "",
			missingConfigError(t, missing),
		},
		{
			"missing check argument",
			[]string{"--check"},
			2, "",
			configCLIUsage + "configfile is required\n",
		},
		{
			"missing format argument",
			[]string{"--format"},
			2, "",
			configCLIUsage + "configfile is required\n",
		},
		{
			"conflicting modes",
			[]string{"--check", valid, "--format"},
			2, "",
			configCLIUsage + "--check and --format cannot be combined\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logDir := filepath.Join(t.TempDir(), "logs")
			args := append([]string{"--logging", logDir}, tc.args...)

			got := runConfigCLI(t, args)
			if got.exit != tc.exit || got.stdout != tc.stdout || got.stderr != tc.stderr {
				t.Errorf(
					"CLI = exit %d, stdout %q, stderr %q; want exit %d, stdout %q, stderr %q",
					got.exit, got.stdout, got.stderr, tc.exit, tc.stdout, tc.stderr,
				)
			}

			_, err := os.Stat(logDir)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("CLI created a log directory: %v", err)
			}
		})
	}
}

func missingConfigError(t *testing.T, path string) string {
	t.Helper()

	// File-open diagnostics include OS-specific error text.
	_, err := os.Stat(path)

	var pathError *os.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathError) {
		t.Fatalf("expected a missing config: %v", err)
	}

	return (&os.PathError{Op: "open", Path: path, Err: pathError.Err}).Error() + "\n"
}

func runConfigCLI(t *testing.T, args []string) configCLIResult {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, append([]string{"--"}, args...)...)
	cmd.Env = append(os.Environ(), configCLIProcessEnv+"=1")

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf(
			"CLI did not finish: %v; stdout %q, stderr %q",
			ctx.Err(),
			stdout.String(),
			stderr.String(),
		)
	}

	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		t.Fatalf("start CLI: %v", err)
	}

	return configCLIResult{
		stdout: stdout.String(),
		stderr: stderr.String(),
		exit:   cmd.ProcessState.ExitCode(),
	}
}
