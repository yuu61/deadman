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

func TestParseArgsSplit(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"short flag", []string{"-c", "2", "deadman.conf"}, 2},
		{"long flag", []string{"--split", "3", "deadman.conf"}, 3},
		{"after config", []string{"deadman.conf", "-c", "2"}, 2},
		{"unset defaults to 0", []string{"deadman.conf"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}

			if opts.Cols != c.want {
				t.Errorf("Cols = %d, want %d", opts.Cols, c.want)
			}
		})
	}
}

func TestResolveCols(t *testing.T) {
	cases := []struct {
		name     string
		cli, cfg int
		want     int
	}{
		{"cli explicit wins over config", 3, 2, 3},
		{"config used when cli unset", 0, 2, 2},
		{"cli used when config unset", 2, 0, 2},
		{"unset stays 0 (New normalizes to 1)", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveCols(c.cli, c.cfg); got != c.want {
				t.Errorf("resolveCols(%d, %d) = %d, want %d", c.cli, c.cfg, got, c.want)
			}
		})
	}
}
