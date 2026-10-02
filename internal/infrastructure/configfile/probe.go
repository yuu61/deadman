package configfile

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// probeInput owns configuration spellings. Only typed values cross into the domain. An
// attribute is present with a non-empty value or absent (parseTarget refuses "key="), so
// "" from take means the attribute was omitted.
type probeInput struct {
	attrs map[string]string
	err   error
}

func (p *probeInput) take(key string) string {
	v := p.attrs[key]
	delete(p.attrs, key)

	return v
}

// port reads port= as an integer. Whether it is in range, or needed, is Compile's call,
// so a written 0 reaches it as a port rather than as an omitted one.
func (p *probeInput) port() probe.Port {
	raw, ok := p.attrs["port"]
	delete(p.attrs, "port")

	if !ok {
		return probe.Port{}
	}

	port, err := parsePort(raw)
	if err != nil {
		p.err = fmt.Errorf("invalid port %q: expected an integer", raw)
	}

	return port
}

// parsePort reads a written port as an integer, leaving its range to Compile.
func parsePort(raw string) (probe.Port, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return probe.Port{}, err
	}

	return probe.PortNumber(n), nil
}

// relayHostPort reads a relay= that may carry a TCP port: HOST, IP, [IP], HOST:PORT or
// [IP]:PORT, an IPv6 address bare or in brackets. The port is read as an integer and the
// host kept as written; whether either is usable is Compile's call.
func (p *probeInput) relayHostPort() (string, probe.Port) {
	raw := p.take("relay")

	host, port, err := splitHostPort(raw)
	if err != nil {
		p.err = fmt.Errorf("invalid relay=%q: %w", raw, err)
	}

	return host, port
}

var (
	errBracketIP = errors.New("expected an IP address in brackets")
	errHostPort  = errors.New("expected HOST:PORT or [IP]:PORT")
	errPortWord  = errors.New("expected a numeric port")
	errNoHost    = errors.New("expected a host before :PORT")
	errZonePort  = errors.New("a zoned IPv6 address with a port goes in brackets: [IP%ZONE]:PORT")
)

// splitHostPort splits a written host and optional port. An omitted relay is "" with no
// port, for Compile to name as missing.
func splitHostPort(raw string) (string, probe.Port, error) {
	if strings.HasPrefix(raw, "[") {
		return splitBracketed(raw)
	}

	a, err := probe.ParseIP(raw)
	if err == nil && strings.Contains(a.Zone(), ":") {
		// A zone takes everything after '%', so a port written after one would be read as
		// part of the interface name.
		return "", probe.Port{}, errZonePort
	}

	if err == nil || !strings.Contains(raw, ":") {
		return raw, probe.Port{}, nil // a name, an IPv4 address or a bare IPv6 one.
	}

	host, rawPort, err := net.SplitHostPort(raw)
	if err != nil {
		return "", probe.Port{}, errHostPort
	}

	if host == "" {
		return "", probe.Port{}, errNoHost
	}

	return withPort(host, rawPort)
}

// splitBracketed reads [IP] or [IP]:PORT: brackets hold an IP address.
func splitBracketed(raw string) (string, probe.Port, error) {
	host, bare := strings.CutSuffix(raw[1:], "]")
	rawPort := ""

	if !bare {
		var err error

		host, rawPort, err = net.SplitHostPort(raw)
		if err != nil {
			return "", probe.Port{}, errHostPort
		}
	}

	_, err := probe.ParseIP(host)
	if err != nil {
		return "", probe.Port{}, errBracketIP
	}

	if bare {
		return host, probe.Port{}, nil
	}

	return withPort(host, rawPort)
}

// withPort pairs host with a written port, which must be an integer.
func withPort(host, rawPort string) (string, probe.Port, error) {
	port, err := parsePort(rawPort)
	if err != nil {
		return "", probe.Port{}, errPortWord
	}

	return host, port, nil
}

// source reads source=: an IP address (a zoned IPv6 one included) is a source address,
// anything else an interface name. This is the one place a source's kind is decided.
func (p *probeInput) source() probe.Source {
	raw, ok := p.attrs["source"]
	delete(p.attrs, "source")

	if !ok {
		return probe.Source{}
	}

	a, err := probe.ParseIP(raw)
	if err == nil {
		return probe.SourceAddr(a)
	}

	return probe.SourceInterface(raw)
}

func (p *probeInput) family() probe.Family {
	raw := p.take("resolve_family")
	switch raw {
	case "":
		return probe.FamilyUnknown
	case "ipv4":
		return probe.FamilyIPv4
	case "ipv6":
		return probe.FamilyIPv6
	default:
		p.err = fmt.Errorf("invalid resolve_family=%q", raw)

		return probe.FamilyUnknown
	}
}

func (p *probeInput) verify() probe.Verification {
	raw, exists := p.attrs["verify"]
	delete(p.attrs, "verify")

	if !exists {
		return probe.VerifyDefault
	}

	on, err := parseBool(raw)
	if err != nil {
		p.err = fmt.Errorf("invalid verify=%q", raw)
	}

	if on {
		return probe.VerifyEnabled
	}

	return probe.VerifyDisabled
}

// gateway reads nexthop= as an IP address; an omitted one is left for Compile to name
// as missing.
func (p *probeInput) gateway() netip.Addr {
	raw := p.take("nexthop")
	if raw == "" {
		return netip.Addr{}
	}

	a, err := probe.ParseIP(raw)
	if err != nil {
		p.err = fmt.Errorf("invalid nexthop=%q: expected IP address", raw)
	}

	return a
}

// parseProbe reads a target's attributes into the parameters of the method probe= names;
// their type is the method. probe= omitted is direct.
func parseProbe(attrs map[string]string) (probe.Params, error) {
	p := probeInput{attrs: attrs}

	name := p.take("probe")
	if name == "" {
		name = probe.MethodDirect.String()
	}

	method, ok := methodNamed(name)
	if !ok {
		return nil, fmt.Errorf("unknown probe=%q", name)
	}

	params := readers[method](&p)
	if p.err != nil {
		return nil, p.err
	}

	if len(attrs) > 0 {
		key := slices.Min(slices.Collect(maps.Keys(attrs)))

		return nil, fmt.Errorf("attribute %q is not supported by probe=%s", key, name)
	}

	return params, nil
}

// methodNamed is the method probe=name selects; ok is false for an unknown name.
func methodNamed(name string) (probe.Method, bool) {
	for _, m := range probe.Methods() {
		if m.String() == name {
			return m, true
		}
	}

	return 0, false
}

// readers reads each method's parameters from a target's attributes: the attributes the
// method takes, into the parameters whose type is that method. A test checks that every
// probe.Methods entry has one and that its parameters compile to that method.
var readers = map[probe.Method]func(p *probeInput) probe.Params{
	probe.MethodDirect: func(p *probeInput) probe.Params {
		return probe.Direct{Source: p.source(), Family: p.family()}
	},
	probe.MethodTCP: func(p *probeInput) probe.Params {
		return probe.TCP{Port: p.port(), Family: p.family()}
	},
	probe.MethodQUIC: func(p *probeInput) probe.Params {
		return probe.QUIC{
			Port:   p.port(),
			ALPN:   p.take("alpn"),
			SNI:    p.take("sni"),
			Verify: p.verify(),
			Family: p.family(),
		}
	},
	probe.MethodSNMP: func(p *probeInput) probe.Params {
		return probe.SNMP{Host: p.take("relay"), Community: p.take("community")}
	},
	probe.MethodSSH: func(p *probeInput) probe.Params {
		return probe.SSH{
			Host:   p.take("relay"),
			Source: p.source(),
			OS:     probe.OS(p.take("os")),
			Family: p.family(),
			User:   p.take("user"),
			Key:    p.take("key"),
		}
	},
	probe.MethodNetns: func(p *probeInput) probe.Params { return probe.Netns(p.namespace()) },
	probe.MethodVRF:   func(p *probeInput) probe.Params { return probe.VRF(p.namespace()) },
	probe.MethodRouterOS: func(p *probeInput) probe.Params {
		host, port := p.relayHostPort()

		return probe.RouterOS{
			Host:     host,
			Port:     port,
			Scheme:   p.take("scheme"),
			Username: p.take("username"),
			Password: p.take("password"),
			Verify:   p.verify(),
		}
	},
	probe.MethodNexthop: func(p *probeInput) probe.Params {
		return probe.Nexthop{Gateway: p.gateway(), Source: p.source()}
	},
}

// namespace reads the attributes netns and vrf share.
func (p *probeInput) namespace() probe.Namespace {
	return probe.Namespace{Name: p.take("relay"), Source: p.source(), Family: p.family()}
}
