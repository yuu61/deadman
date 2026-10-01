package probe

import (
	"fmt"
	"strconv"
)

// maxPort is the largest destination port.
const maxPort = 65535

// Port is a destination port as a target writes it: a number, or omitted (the zero
// value). Omission is its own state, so a written 0 is an invalid port rather than a
// request for the default. Compile fills an omitted port with its method's default or
// rejects it where the method needs one, and checks a written one is 1-65535, so a
// plan's port is always a valid number.
type Port struct {
	n   int
	set bool
}

// PortNumber is port n as written.
func PortNumber(n int) Port { return Port{n: n, set: true} }

func (p Port) String() string { return strconv.Itoa(p.n) }

// resolve fills an omitted port with def and checks the range. An omitted def means the
// method has no default, so the port is required.
func (p Port) resolve(m Method, def Port) (Port, error) {
	if !p.set {
		if !def.set {
			return Port{}, fmt.Errorf("%s requires port", m)
		}

		p = def
	}

	if p.n < 1 || p.n > maxPort {
		return Port{}, fmt.Errorf("invalid port %d (want 1-%d)", p.n, maxPort)
	}

	return p, nil
}
