// Package config parses deadman configuration files into target specs.
//
// The grammar is kept byte-for-byte compatible with the original Python
// implementation (gettargetlist): tabs become spaces, runs of whitespace are
// collapsed, full-line "#" comments and ";#" trailers are stripped, blank lines
// are skipped, and the remaining "NAME ADDRESS key=value..." tokens are split on
// single spaces. A name matching ^-+$ denotes a visual separator.
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
	reSpaces      = regexp.MustCompile(`\s+`)
	reComment     = regexp.MustCompile(`^#.*`)
	reSemiComment = regexp.MustCompile(`;\s*#`)
	reSeparator   = regexp.MustCompile(`^-+$`)
)

// relayKeys mirrors the set of attributes the original stored in the relay dict.
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
		line = reSpaces.ReplaceAllString(line, " ")
		line = reComment.ReplaceAllString(line, "")
		line = reSemiComment.ReplaceAllString(line, "")

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Split(line, " ")

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
// ignored, matching the original.
func applyAttr(spec *TargetSpec, kv string) {
	p := strings.SplitN(kv, "=", 2)
	if len(p) != 2 {
		return
	}

	key, value := p[0], p[1]

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
	p := strings.SplitN(kv, "=", 2)
	if len(p) != 2 {
		return
	}

	switch strings.ToLower(p[1]) {
	case "on", "true", "yes", "1":
		cols[strings.ToUpper(p[0])] = true
	case "off", "false", "no", "0":
		cols[strings.ToUpper(p[0])] = false
	default:
		// unknown bool spelling: ignored.
	}
}
