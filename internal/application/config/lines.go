package config

import "github.com/yuu61/deadman/internal/domain/probe"

// Line is one line of the target list, in config order: a separator, a target line whose
// attributes describe a probe, or one whose attributes describe none. Each kind carries
// only what it has.
//
//sumtype:decl
type Line interface{ line() }

// Separator is a visual separator row with an optional label.
type Separator struct {
	Label string
}

// Target is a target line and the probe its attributes describe: the parameters of the
// method probe= names, whose type is the method. No parameters is a direct probe.
type Target struct {
	Name   string
	Addr   string
	Params probe.Params
}

// Malformed is a target line whose attributes describe no probe — an unknown probe=, an
// attribute its method does not take, a repeated or empty attribute, a value of the
// wrong syntax. Problem says which.
type Malformed struct {
	Name    string
	Addr    string
	Problem string
}

// ProbeSpec maps the target to the probe.Spec that probe.Compile turns into the plan the
// row is built, warned about and labeled by.
func (t Target) ProbeSpec() probe.Spec {
	return probe.Spec{Addr: t.Addr, Params: t.Params}
}

func (Separator) line() {}
func (Target) line()    {}
func (Malformed) line() {}

// Note is what the parser had to guess on one line, for the frontend to warn about
// rather than fail silently: the bare words after the address it dropped (an unquoted
// name with spaces, e.g. "Cloudflare via MGMT 1.1.1.1 ...", shifts real tokens here;
// attributes are key=value), and whether a double quote was left open, so the rest of
// the line was absorbed into a single field.
type Note struct {
	Name              string
	Addr              string
	Dropped           []string
	UnterminatedQuote bool
}
