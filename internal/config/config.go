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
	"strconv"
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

// Config is the parsed configuration: the target list plus optional display
// settings from directive lines. Columns holds only the columns explicitly named
// (absent columns keep their default, shown); Scale/Precision are zero/empty when
// their directive is absent, letting the caller fall back to the CLI/default.
type Config struct {
	Targets   []TargetSpec
	Columns   map[string]bool // column key (upper-case) -> shown.
	Scale     int             // RTT-bar ms-per-step from a "scale" directive; 0 = unset.
	Precision string          // stat-precision label from a "precision" directive; "" = unset.
	Cols      int             // newspaper-column count from a "split" directive; 0 = unset.
}

// Directive keywords. A line whose first field is one of these is a global display
// setting rather than a target (a host named like a directive is not expected).
const (
	columnDirective    = "columns"
	scaleDirective     = "scale"
	precisionDirective = "precision"
	splitDirective     = "split"
)

// directives maps a keyword to its handler, applied to the line's remaining fields.
// Handlers are lenient — malformed or out-of-range tokens are ignored, matching
// applyColumn/applyAttr — so a typo degrades to the default rather than aborting the
// parse. The precision label is stored verbatim and validated by the TUI (its
// precisionModes table is the single source of valid labels), keeping config free of
// a duplicate list.
var directives = map[string]func(cfg *Config, args []string){
	columnDirective: func(cfg *Config, args []string) {
		for _, kv := range args {
			applyColumn(cfg.Columns, kv)
		}
	},
	scaleDirective: func(cfg *Config, args []string) {
		if len(args) == 0 {
			return
		}

		n, err := strconv.Atoi(args[0])
		if err == nil && n > 0 {
			cfg.Scale = n
		}
	},
	precisionDirective: func(cfg *Config, args []string) {
		if len(args) > 0 {
			cfg.Precision = args[0]
		}
	},
	splitDirective: func(cfg *Config, args []string) {
		if len(args) == 0 {
			return
		}

		n, err := strconv.Atoi(args[0])
		if err == nil && n > 0 {
			cfg.Cols = n
		}
	},
}

var reSeparator = regexp.MustCompile(`^-+$`)

// relayKeys is the set of attribute keys routed into a target's relay map. "netns" is
// deliberately absent: the netns mode is selected by via=netns and reads the namespace
// name from relay=, so a `netns=` token is a typo that should surface as an ignored
// stray token (the warning every other unknown key gets), not be silently swallowed.
// "port"/"alpn"/"sni" are QUIC (via=quic) sub-attributes; like "method"/"verify" they
// are meaningful only to their mode and harmlessly ignored by the others.
var relayKeys = map[string]bool{
	"os": true, "relay": true, "via": true, "community": true,
	"user": true, "key": true, "method": true,
	"username": true, "password": true, "verify": true,
	"nexthop": true, "resolve_family": true,
	"port": true, "alpn": true, "sni": true,
}

// ParseConfig reads a deadman config from r and returns the parsed targets plus
// any column-visibility overrides.
func ParseConfig(r io.Reader) (Config, error) {
	cfg := Config{Columns: map[string]bool{}}

	sc := bufio.NewScanner(r)

	first := true

	for sc.Scan() {
		line := strings.ReplaceAll(sc.Text(), "\t", " ")
		if first {
			// Strip a leading UTF-8 BOM (U+FEFF) from the first line only. Windows
			// editors (Notepad/PowerShell) routinely save it, and otherwise it sticks to
			// the first token — turning a directive like "scale 5" into a phantom target
			// or garbling the first host's name.
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}

		line = stripComment(line)

		fields, terminated := tokenize(line)
		if len(fields) == 0 {
			continue
		}

		// Directive keywords are matched case-insensitively (like the column on/off
		// values), so a capitalized "Scale 5" applies the setting instead of silently
		// becoming a phantom target.
		if h, ok := directives[strings.ToLower(fields[0])]; ok {
			h(&cfg, fields[1:])

			continue
		}

		spec := parseTarget(fields)
		spec.UnterminatedQuote = !terminated
		cfg.Targets = append(cfg.Targets, spec)
	}

	return cfg, sc.Err()
}

// stripComment removes a comment from a config line: a whole-line comment (the
// first non-blank rune is '#', after any indentation) yields "", and a ';#' trailer
// (a ';' followed by optional spaces and a '#', outside any double-quoted span)
// truncates the line at the ';'. It is quote-aware, so a '#'/';#' inside a quoted
// attribute value is preserved — quote a value to keep a literal ';#'. Tabs are
// already collapsed to spaces by the caller before this runs.
func stripComment(line string) string {
	if t := strings.TrimLeft(line, " \t"); t == "" || t[0] == '#' {
		return "" // blank line or whole-line comment.
	}

	inQuote := false

	for i, r := range line {
		switch r {
		case '"':
			inQuote = !inQuote
		case ';':
			if inQuote {
				continue
			}

			if rest := strings.TrimLeft(line[i+1:], " "); strings.HasPrefix(rest, "#") {
				return line[:i]
			}
		default:
			// Ordinary content rune.
		}
	}

	return line
}

// tokenize splits a config line into whitespace-separated fields, honoring
// double-quoted spans so a field (typically the name) may contain spaces:
//
//	"Cloudflare via MGMT" 1.1.1.1 nexthop=10.98.38.9
//
// The surrounding quotes are removed and whitespace inside them is preserved;
// quoting works mid-token too (key="/a b" yields key=/a b). It replaces
// strings.Fields and behaves identically for unquoted input. stripComment is
// quote-aware, so quotes DO protect a '#'/';#' comment marker inside a value. Only
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
