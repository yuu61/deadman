package probe

import (
	"errors"
	"strconv"
	"strings"
)

// Plan is a validated, fully resolved probe. Its value parameters contain no mutable
// references; execution and identity use the same conditions. Only Compile makes one: the
// zero Plan probes nothing and has no method.
type Plan struct {
	dest   Destination
	params Params
}

// Method returns the selected probing method, which the parameters' type is.
func (p Plan) Method() Method { return p.params.method() }

// Destination returns the target: a host name as written, or an IP address in its
// canonical spelling (an IPv4-mapped one as IPv4).
func (p Plan) Destination() Destination { return p.dest }

// Params returns the resolved method parameters.
func (p Plan) Params() Params { return p.params }

// Params is the closed set of method parameters, used as input and resolved by Compile.
// The type is the method: each one names the method it is the parameters of, knows how
// Compile checks and fills it in, and which of its fields make up the Identity. A method
// and parameters of another method can therefore never be paired.
//
//sumtype:decl
type Params interface {
	method() Method
	compile(dest Destination) (Params, error)
	identity(b *strings.Builder)
}

// Compile validates typed input and resolves all defaults without performing I/O. Spec
// without parameters is a direct probe, like a target line without probe=.
func Compile(s Spec) (Plan, error) {
	if s.Addr == "" {
		return Plan{}, errors.New("probe address is required")
	}

	dest, err := parseDestination(s.Addr)
	if err != nil {
		return Plan{}, err
	}

	if s.Params == nil {
		s.Params = Direct{}
	}

	params, err := s.Params.compile(dest)
	if err != nil {
		return Plan{}, err
	}

	return Plan{dest: dest, params: params}, nil
}

// Identity is the monitored path's semantic identity: the method, the address and the
// parameters that tell the method's paths apart. It excludes display names, credentials
// and reads the filled-in defaults, so an omitted
// attribute and its default spelled out (quic without port= and with port=443) are one
// path.
func (p Plan) Identity() string {
	var b strings.Builder

	writeKey(&b, p.Method().String())
	writeKey(&b, p.dest.String())
	p.params.identity(&b)

	return b.String()
}

// writeKey appends one quoted identity field, so a delimiter inside a value (an IPv6
// address, a source) can never make two different field lists spell the same identity.
func writeKey(b *strings.Builder, value string) {
	b.WriteString(strconv.Quote(value))
	b.WriteByte(':')
}
