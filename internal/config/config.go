// Package config parses deadman configuration files into target specs.
//
// The grammar: tabs become spaces, full-line "#" comments and ";#" trailers are
// stripped, blank lines are skipped, and the remaining "NAME ADDRESS
// key=value..." line is split into fields on whitespace. A field may be wrapped in
// double quotes to include spaces — most usefully the name ("My Host" 1.2.3.4) —
// and the quotes are removed. A name matching ^-+$ denotes a visual separator.
package config

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// TargetSpec is one parsed line of the config file.
type TargetSpec struct {
	Name        string
	Addr        string
	Source      string
	TCP         string
	Relay       map[string]string
	IsSeparator bool
	// Dropped holds post-address tokens that were silently ignored: bare words
	// (no "=") and unknown key=value attributes. A name with spaces (e.g.
	// "Cloudflare via MGMT 1.1.1.1 ...") shifts real tokens here, so the TUI can
	// warn instead of failing silently.
	Dropped []string
	// UnterminatedQuote is true when the line had an opening double quote with no
	// closing one, so the rest of the line was absorbed into a single field. The
	// TUI warns rather than silently mis-binding the attribute.
	UnterminatedQuote bool
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
	reComment = regexp.MustCompile(`^#.*`)
	// reSemiComment matches a ";#" trailer and everything after it, so the comment
	// text is removed rather than left as stray tokens.
	reSemiComment = regexp.MustCompile(`;\s*#.*`)
	reSeparator   = regexp.MustCompile(`^-+$`)
)

// relayKeys is the set of attribute keys routed into a target's relay map.
var relayKeys = map[string]bool{
	"os": true, "relay": true, "via": true, "community": true,
	"netns": true, "user": true, "key": true, "method": true,
	"username": true, "password": true, "verify": true,
	"nexthop": true,
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

		fields, terminated := tokenize(line)
		if len(fields) == 0 {
			continue
		}

		if fields[0] == columnDirective {
			for _, kv := range fields[1:] {
				applyColumn(cfg.Columns, kv)
			}

			continue
		}

		spec := parseTarget(fields)
		spec.UnterminatedQuote = !terminated
		cfg.Targets = append(cfg.Targets, spec)
	}

	return cfg, sc.Err()
}

// tokenize splits a config line into whitespace-separated fields, honoring
// double-quoted spans so a field (typically the name) may contain spaces:
//
//	"Cloudflare via MGMT" 1.1.1.1 nexthop=10.98.38.9
//
// The surrounding quotes are removed and whitespace inside them is preserved;
// quoting works mid-token too (key="/a b" yields key=/a b). It replaces
// strings.Fields and behaves identically for unquoted input. Comment stripping
// runs before tokenize, so quotes do not protect a '#'/';#' comment marker. Only
// the double quote is special; a single quote is a literal character.
//
// terminated is false when an opening quote had no closing one: the rest of the
// line is then absorbed into the final field, and the caller surfaces a warning
// rather than mis-binding it silently.
func tokenize(line string) ([]string, bool) {
	var (
		tokens  []string
		cur     strings.Builder
		inQuote bool
		started bool // current token has begun (covers an empty quoted "").
	)

	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote

			started = true
		case inQuote:
			cur.WriteRune(r)
		case unicode.IsSpace(r):
			if started {
				tokens = append(tokens, cur.String())
				cur.Reset()

				started = false
			}
		default:
			cur.WriteRune(r)

			started = true
		}
	}

	if started {
		tokens = append(tokens, cur.String())
	}

	return tokens, !inQuote
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
// in the relay map; source and tcp have their own fields. Tokens that cannot be
// routed — bare words with no "=" and unknown keys — are recorded in spec.Dropped
// so the caller can warn rather than dropping them silently.
func applyAttr(spec *TargetSpec, kv string) {
	key, value, ok := strings.Cut(kv, "=")
	if !ok {
		spec.Dropped = append(spec.Dropped, kv) // bare word (e.g. a name with spaces).

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
		spec.Dropped = append(spec.Dropped, kv) // unknown attribute key.
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
