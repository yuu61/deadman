package monitoring

import (
	"slices"

	"github.com/yuu61/deadman/internal/application/config"
)

// Diagnostic is a fact about a monitoring or configuration problem, independent of
// terminal layout or language: application selects it, presentation words it. Each kind
// carries only the facts its wording needs.
//
//sumtype:decl
type Diagnostic interface{ diagnostic() }

// UnterminatedQuote is a config line whose double quote was left open, so the rest of the
// line was absorbed into one field.
type UnterminatedQuote struct{ Name string }

// DroppedTokens is a config line with bare words after its address, which the parser
// dropped: typically an unquoted name with spaces.
type DroppedTokens struct {
	Name   string
	Addr   string
	Tokens []string
}

// TargetBuildFailed is a target that could not be built. Reason is its error as written
// below presentation: the one kind of diagnostic text that comes from a lower layer, since
// it names what the config got wrong (probe.Compile's and each adapter's own checks).
type TargetBuildFailed struct {
	Name   string
	Addr   string
	Reason string
}

// ReloadFailed is a reload whose config could not be read; the current targets go on.
// Reason is the read error as written.
type ReloadFailed struct{ Reason string }

func (UnterminatedQuote) diagnostic() {}
func (DroppedTokens) diagnostic()     {}
func (TargetBuildFailed) diagnostic() {}
func (ReloadFailed) diagnostic()      {}

// noteWarnings reports what the parser had to guess on the config lines: an
// unterminated quote, and tokens it could not route.
func noteWarnings(notes []config.Note) []Diagnostic {
	var warns []Diagnostic

	for _, n := range notes {
		if n.UnterminatedQuote {
			warns = append(warns, UnterminatedQuote{Name: n.Name})
		}

		if len(n.Dropped) > 0 {
			warns = append(warns, DroppedTokens{
				Name: n.Name, Addr: n.Addr, Tokens: slices.Clone(n.Dropped),
			})
		}
	}

	return warns
}
