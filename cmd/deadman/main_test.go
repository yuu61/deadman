package main

import "testing"

func TestParseArgsAsync(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		async bool
		path  string
	}{
		{"flag before config", []string{"-a", "deadman.conf"}, true, "deadman.conf"},
		{"flag after config", []string{"deadman.conf", "-a"}, true, "deadman.conf"},
		{"long form", []string{"--async-mode", "deadman.conf"}, true, "deadman.conf"},
		{"no flag", []string{"deadman.conf"}, false, "deadman.conf"},
		{"mixed", []string{"-s", "20", "deadman.conf", "-a", "-l", "logs"}, true, "deadman.conf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if opts.Async != c.async {
				t.Errorf("Async = %v, want %v", opts.Async, c.async)
			}

			if opts.ConfigPath != c.path {
				t.Errorf("ConfigPath = %q, want %q", opts.ConfigPath, c.path)
			}
		})
	}
}

func TestParseArgsScaleBlinkLog(t *testing.T) {
	opts, err := parseArgs([]string{"deadman.conf", "-s", "20", "-b", "-l", "logs"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.Scale != 20 {
		t.Errorf("Scale = %d, want 20", opts.Scale)
	}

	if !opts.Blink {
		t.Error("Blink = false, want true")
	}

	if opts.LogDir != "logs" {
		t.Errorf("LogDir = %q, want logs", opts.LogDir)
	}
}

func TestParseArgsMissingConfig(t *testing.T) {
	_, err := parseArgs([]string{"-a"})
	if err == nil {
		t.Error("expected error when configfile is missing")
	}
}

func TestResolveScale(t *testing.T) {
	cases := []struct {
		name     string
		cli, cfg int
		want     int
	}{
		{"cli explicit wins over config", 20, 5, 20},
		{"config used when cli unset", 0, 5, 5},
		{"cli used when config unset", 7, 0, 7},
		{"default when both unset", 0, 0, defaultScale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveScale(c.cli, c.cfg); got != c.want {
				t.Errorf("resolveScale(%d, %d) = %d, want %d", c.cli, c.cfg, got, c.want)
			}
		})
	}
}
