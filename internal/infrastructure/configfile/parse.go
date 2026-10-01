// Package configfile reads deadman configuration files into a config.Config.
//
// The grammar: tabs become spaces, full-line "#" comments and ";#" trailers are
// stripped, blank lines are skipped, and the remaining "NAME ADDRESS
// key=value..." line is split into fields on whitespace. A field may be wrapped in
// double quotes to include spaces — most usefully the name ("My Host" 1.2.3.4) —
// and the quotes are removed. A name matching ^-+$ denotes a visual separator.
package configfile

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuu61/deadman/internal/application/config"
)

// Directive keywords. A line whose first field is one of these is a global display
// setting rather than a target (a host named like a directive is not expected).
const (
	columnDirective    = "columns"
	scaleDirective     = "scale"
	precisionDirective = "precision"
	splitDirective     = "split"
	glyphDirective     = "glyph"
)

// directives maps a keyword to its handler, applied to the line's remaining fields.
// Handlers check syntax only: a well-formed value is recorded as written (a later line
// of the same directive overrides it), and a malformed or missing one is ignored, so a
// typo degrades to the default rather than aborting the parse. Whether a value is
// usable — a scale within range, a known precision or glyph name, a positive split — is
// the frontend's call, since the frontend owns those vocabularies.
var directives = map[string]func(d *config.Display, args []string){
	columnDirective: func(d *config.Display, args []string) {
		for _, kv := range args {
			applyColumn(d.Columns, kv)
		}
	},
	scaleDirective: func(d *config.Display, args []string) {
		if len(args) == 0 {
			return
		}

		n, err := strconv.ParseFloat(args[0], 64)
		if err == nil {
			d.Scale = n
		}
	},
	precisionDirective: func(d *config.Display, args []string) {
		if len(args) > 0 {
			d.Precision = args[0]
		}
	},
	glyphDirective: func(d *config.Display, args []string) {
		if len(args) > 0 {
			d.Glyph = args[0]
		}
	},
	splitDirective: func(d *config.Display, args []string) {
		if len(args) == 0 {
			return
		}

		n, err := strconv.Atoi(args[0])
		if err == nil {
			d.Cols = n
		}
	},
}

var reSeparator = regexp.MustCompile(`^-+$`)

// errUnknownBool reports a word that is neither a truthy nor a falsy spelling.
var errUnknownBool = errors.New("configfile: unrecognized boolean word")

// parseBool reads a boolean word of the config language, case-insensitively: truthy
// on/true/yes/1, falsy off/false/no/0. Any other word (including "") returns
// errUnknownBool, so a caller keeps its own default and can tell "explicitly set" from
// "absent". The "columns" directive and the boolean target attributes (verify=) share
// it, so the file has one boolean vocabulary.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, errUnknownBool
	}
}

// Parse reads a deadman config from r and returns the parsed targets plus the display
// directives.
func Parse(r io.Reader) (config.Config, error) {
	cfg := config.Config{Display: config.Display{Columns: map[string]bool{}}}

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
			h(&cfg.Display, fields[1:])

			continue
		}

		target, note := parseTarget(fields)
		note.UnterminatedQuote = !terminated

		cfg.Lines = append(cfg.Lines, target)

		if note.UnterminatedQuote || len(note.Dropped) > 0 {
			cfg.Notes = append(cfg.Notes, note)
		}
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

// parseTarget reads the whitespace-split fields of one non-directive config line into its
// line, and the note on what the parser had to guess there (the caller adds whether a
// quote was left open).
func parseTarget(fields []string) (config.Line, config.Note) {
	note := config.Note{Name: fields[0]}
	if len(fields) > 1 {
		note.Addr = fields[1]
	}

	if reSeparator.MatchString(note.Name) {
		return config.Separator{}, note
	}

	attrs, dropped, problem := parseAttributes(fields[min(2, len(fields)):])
	note.Dropped = dropped

	params, err := parseProbe(attrs)
	if problem == "" && err != nil {
		problem = err.Error()
	}

	if problem != "" {
		return config.Malformed{Name: note.Name, Addr: note.Addr, Problem: problem}, note
	}

	return config.Target{Name: note.Name, Addr: note.Addr, Params: params}, note
}

// parseAttributes collects a target line's key=value fields, and the bare words (no "=")
// it drops. The first repeated key or empty value is the line's problem. A value is never
// empty, so an attribute is either written with one or omitted — no attribute has a
// third, "written empty" state that a default could silently take over.
func parseAttributes(fields []string) (map[string]string, []string, string) {
	attrs := map[string]string{}

	var (
		dropped []string
		problem string
	)

	for _, kv := range fields {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			dropped = append(dropped, kv)

			continue
		}

		if _, duplicate := attrs[key]; duplicate && problem == "" {
			problem = "duplicate attribute: " + key
		}

		if value == "" && problem == "" {
			problem = fmt.Sprintf("attribute %q needs a value", key)
		}

		attrs[key] = value
	}

	return attrs, dropped, problem
}

// applyColumn parses one "KEY=on|off" token from a "columns" directive and records
// the column's visibility (key upper-cased) in cols. on/off, true/false, yes/no and
// 1/0 are accepted (case-insensitive); malformed tokens and unknown bool spellings
// are ignored for display directives.
func applyColumn(cols map[string]bool, kv string) {
	key, val, ok := strings.Cut(kv, "=")
	if !ok {
		return
	}

	// Unknown display booleans are ignored (no map entry).
	v, err := parseBool(val)
	if err != nil {
		return
	}

	cols[strings.ToUpper(key)] = v
}

// Load opens and parses the config file at path. Its errors are the file's open and
// read errors as they are, so the CLI and a failed reload report them unchanged.
func Load(ctx context.Context, path string) (config.Config, error) {
	err := ctx.Err()
	if err != nil {
		return config.Config{}, err
	}

	// #nosec G304 -- path is the operator-supplied config file, not remote input.
	f, err := os.Open(path)
	if err != nil {
		return config.Config{}, err
	}
	defer func() { _ = f.Close() }()

	// A local file reads in an instant, and a read that blocks could not be interrupted
	// by a check between reads anyway: a reload canceled meanwhile is discarded by its
	// caller.
	return Parse(f)
}
