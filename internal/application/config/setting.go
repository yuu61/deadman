package config

// Setting is one explicitly written display setting. Unlike Display's effective
// values, it distinguishes a written zero or empty string from an omitted setting.
// The frontend checks its own value ranges and vocabulary without parsing text.
//
//sumtype:decl
type Setting interface{ setting() }

// Scale is a written RTT-bar scale in milliseconds.
type Scale float64

// Split is a written number of display columns.
type Split int

// Precision is a written statistics precision name.
type Precision string

// Glyph is a written result-bar glyph set name.
type Glyph string

// Column is a written column visibility setting.
type Column struct {
	Key     string
	Visible bool
}

func (Scale) setting()     {}
func (Split) setting()     {}
func (Precision) setting() {}
func (Glyph) setting()     {}
func (Column) setting()    {}
