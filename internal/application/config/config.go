// Package config defines application input DTOs: the lines of the target list as parsed,
// the parser's notes on how they were written, and the display directives as written.
// These are transport data, not monitoring entities. Target.ProbeSpec maps a target line
// to the domain's input; whether a display directive's value is usable is the frontend's
// call.
package config

// Display holds the display directives of a config file as written. The parser checks
// only their syntax — a number where a number is expected — and the last well-formed
// line of a directive wins; the frontend validates each value and falls back to its
// default for one it cannot use. A zero value means the directive is absent (or was
// never well-formed), letting the frontend fall back to the CLI flag or its default.
type Display struct {
	Columns   map[string]bool // column key (upper-case) -> shown; only the columns named.
	Scale     float64         // "scale": RTT-bar ms-per-step (or log-mode floor).
	Precision string          // "precision": stat-precision label.
	Glyph     string          // "glyph": RESULT-bar glyph set name.
	Cols      int             // "split": newspaper-column count.
}

// Config is the parsed configuration: the target list, the notes on how its lines were
// written (in line order, only for lines with something to note), and the display
// directives.
type Config struct {
	Lines   []Line
	Notes   []Note
	Display Display
}
