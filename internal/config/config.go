// Package config parses deadman configuration files into target specs.
//
// The grammar: tabs become spaces, runs of whitespace are collapsed, full-line
// "#" comments and ";#" trailers are stripped, blank lines are skipped, and the
// remaining "NAME ADDRESS key=value..." tokens are split on single spaces. A name
// matching ^-+$ denotes a visual separator.
package config

import (
	"bufio"
	"io"
	"regexp"
	"strings"
)

// TargetSpec is one parsed line of the config file.
type TargetSpec struct {
	Name        string
	Addr        string
	Source      string
	TCP         string
	Relay       map[string]string
	IsSeparator bool
}

// Config is the parsed configuration: the target list plus optional column
// visibility overrides from "columns" directive lines. Columns holds only the
// columns explicitly named; absent columns keep their default (shown).
type Config struct {
	Targets []TargetSpec
	Columns map[string]bool // column key (upper-case) -> shown.
}

// columnDirective is the first token of a column-visibility line:
// "columns KEY=on KEY=off ..." (a host named "columns" is not expected).
const columnDirective = "columns"

var (
	reComment     = regexp.MustCompile(`^#.*`)
	reSemiComment = regexp.MustCompile(`;\s*#`)
	reSeparator   = regexp.MustCompile(`^-+$`)
)

// relayKeys is the set of attribute keys routed into a target's relay map.
var relayKeys = map[string]bool{
	"os": true, "relay": true, "via": true, "community": true,
	"netns": true, "user": true, "key": true, "method": true,
	"username": true, "password": true, "verify": true,
}

// ParseConfig reads a deadman config from r and returns the parsed targets plus
// any column-visibility overrides.
func ParseConfig(r io.Reader) (Config, error) {
	cfg := Config{Columns: map[string]bool{}}

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		line = strings.ReplaceAll(line, "\t", " ")
		line = reComment.ReplaceAllString(line, "")
		line = reSemiComment.ReplaceAllString(line, "")

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if fields[0] == columnDirective {
			for _, kv := range fields[1:] {
				applyColumn(cfg.Columns, kv)
			}

			continue
		}

		cfg.Targets = append(cfg.Targets, parseTarget(fields))
	}

	return cfg, sc.Err()
}

// parseTarget builds a TargetSpec from the whitespace-split fields of one
// non-directive config line.
func parseTarget(fields []string) TargetSpec {
	spec := TargetSpec{Name: fields[0], Relay: map[string]string{}}
	if len(fields) > 1 {
		spec.Addr = fields[1]
	}

	if len(fields) > 2 {
		for _, kv := range fields[2:] {
			applyAttr(&spec, kv)
		}
	}

	if reSeparator.MatchString(spec.Name) {
		spec.IsSeparator = true
	}

	return spec
}

// applyAttr parses one "key=value" token and stores it on spec. Relay keys land
// in the relay map; source and tcp have their own fields; unknown keys are
// ignored.
func applyAttr(spec *TargetSpec, kv string) {
	key, value, ok := strings.Cut(kv, "=")
	if !ok {
		return
	}

	switch {
	case relayKeys[key]:
		spec.Relay[key] = value
	case key == "source":
		spec.Source = value
	case key == "tcp":
		spec.TCP = value
	default:
		// unknown attribute key: ignored.
	}
}

// applyColumn parses one "KEY=on|off" token from a "columns" directive and records
// the column's visibility (key upper-cased) in cols. on/off, true/false, yes/no and
// 1/0 are accepted (case-insensitive); malformed tokens and unknown bool spellings
// are ignored, matching applyAttr's lenient handling.
func applyColumn(cols map[string]bool, kv string) {
	key, val, ok := strings.Cut(kv, "=")
	if !ok {
		return
	}

	switch strings.ToLower(val) {
	case "on", "true", "yes", "1":
		cols[strings.ToUpper(key)] = true
	case "off", "false", "no", "0":
		cols[strings.ToUpper(key)] = false
	default:
		// unknown bool spelling: ignored.
	}
}
