package probe

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// The parameters of the methods whose probe a relay sends: SNMP (an agent), SSH (a
// host's ping) and RouterOS (a REST API) here, and Netns and VRF (a namespace's or VRF's
// ping on this host) in namespace.go. Each type sits with how Compile fills it in and
// which of its fields make up the Identity.

// RelayParams is the closed set of the parameters of the methods whose probe a relay
// sends, as opposed to LocalParams.
//
//sumtype:decl
type RelayParams interface {
	Params
	relayed()
}

// defaultRouterOSScheme is the transport used when RouterOS has no explicit scheme.
const defaultRouterOSScheme = "https"

// Default TCP ports of the REST API.
const (
	defaultRouterOSHTTPPort  = 80
	defaultRouterOSHTTPSPort = 443
)

// SNMP asks an SNMP agent to ping the target by the remote ping of RFC 4560 (MethodSNMP).
// The target is resolved on this host (its first address, of either family) and the
// agent is handed only the address bytes, so a zone, which names an interface of this
// host, never reaches the agent. The method is deprecated: few agents implement that
// MIB.
type SNMP struct {
	Host      string // relay=: the agent that sends the ping.
	Community string // community=: a credential, so no part of the identity.
}

func (SNMP) relayed() {}

func (SNMP) method() Method { return MethodSNMP }

func (s SNMP) compile(dest Destination) (Params, error) {
	if s.Host == "" || s.Community == "" {
		return nil, errors.New("snmp requires relay and community")
	}

	// A Net-SNMP transport spec (udp6:[::1]:161, host:1161) would be dialed as a host
	// name, failing every round.
	if _, ok := ipLiteral(s.Host); !ok && !isHostName(s.Host) {
		return nil, fmt.Errorf(
			"snmp relay %q must be a host name or IP address (the agent's UDP port 161)",
			s.Host,
		)
	}

	if a, ok := dest.IP(); ok && a.Zone() != "" {
		return nil, fmt.Errorf(
			"snmp cannot give the agent the zone of %s; the agent pings without it",
			a,
		)
	}

	return s, nil
}

func (s SNMP) identity(b *strings.Builder) { writeKey(b, s.Host) }

// SSH runs the system ping on another host over ssh (MethodSSH).
type SSH struct {
	Host string // relay=: the ssh host.
	// Source is source=: the ping's source on the relay. Which kinds its ping takes depends
	// on OS: an address or an interface on Linux, an address on Darwin, none on FreeBSD.
	Source Source
	// Family is resolve_family=: the family the relay's ping runs in. A literal or a source
	// address fixes it; a host name without a source address needs it written.
	Family Family
	OS     OS     // os=: the relay's ping dialect; required.
	User   string // user=: the ssh login user.
	Key    string // key=: the ssh identity file.
}

func (SSH) relayed() {}

func (SSH) method() Method { return MethodSSH }

func (s SSH) compile(dest Destination) (Params, error) {
	if s.Host == "" {
		return nil, errors.New("ssh requires relay")
	}

	err := checkName("ssh relay", s.Host)
	if err != nil {
		return nil, err
	}

	s, err = s.normalizeRelay()
	if err != nil {
		return nil, err
	}

	switch s.OS {
	case OSLinux, OSDarwin, OSFreeBSD:
	case OSWindows:
		return nil, errors.New("unsupported Windows relay ping")
	default:
		return nil, errors.New("ssh requires os=Linux, Darwin or FreeBSD")
	}

	src, err := relaySource(s.OS, s.Source)
	if err != nil {
		return nil, err
	}

	f, err := relayFamily(dest, s.Family, src)
	if err != nil {
		return nil, err
	}

	s.Source, s.Family = src, f

	return s, nil
}

// OpenSSH's inline user overrides -l. Resolve it before deriving the row identity.
func (s SSH) normalizeRelay() (SSH, error) {
	user, host, inline := strings.Cut(s.Host, "@")
	if !inline {
		return s, nil
	}

	if user == "" || host == "" || strings.Contains(host, "@") {
		return s, fmt.Errorf("invalid ssh relay %q: expected user@host", s.Host)
	}

	s.User, s.Host = user, host

	return s, nil
}

func (s SSH) identity(b *strings.Builder) {
	writeKey(b, s.Host)
	writeKey(b, s.Source.String())
	writeKey(b, string(s.OS))
	writeKey(b, s.Family.resolveSpelling())
}

// relaySource checks a source against the relay ping's dialect: Linux's -I takes an
// address or an interface, Darwin's -S only an address, and no other dialect takes one.
func relaySource(os OS, s Source) (Source, error) {
	src, err := s.normalize()
	if err != nil || !src.IsSet() {
		return src, err
	}

	switch os {
	case OSLinux:
		return src, nil
	case OSDarwin:
		if name, ok := src.Interface(); ok {
			return Source{}, fmt.Errorf(
				"interface-name source %q is not supported on a Darwin relay; use a source address",
				name,
			)
		}

		return src, nil
	case OSFreeBSD, OSWindows:
		return Source{}, fmt.Errorf("source is not supported on a %s relay", os)
	default:
		// Unreachable: Compile admits only the dialects above before checking a source.
		return Source{}, fmt.Errorf("unknown relay OS %q", os)
	}
}

// relayFamily fixes the family the relay's ping runs in without asking this host's DNS:
// a literal's, the explicit one, or a source address's. A host name with none of these
// cannot choose ping -4 or -6.
func relayFamily(dest Destination, explicit Family, src Source) (Family, error) {
	f, err := probeFamily(dest, explicit, src)
	if err != nil {
		return 0, err
	}

	if f == FamilyUnknown {
		return 0, fmt.Errorf("relay hostname %q requires resolve_family", dest)
	}

	return f, nil
}

// RouterOS asks a RouterOS REST API to ping the target (MethodRouterOS).
type RouterOS struct {
	// Host is relay='s host: a host name, or an IP address. The adapter puts it in a URL
	// as written.
	Host string
	// Port is relay='s :PORT, or omitted for the scheme's default (443, or 80 for http).
	Port     Port
	Scheme   string // "http", or "https" by default.
	Username string // username= and password=: credentials, so no part of the identity.
	Password string
	// Verify is verify=, on by default: self-signed certificates are common on network
	// gear, but turning the check off must be a deliberate choice.
	Verify Verification
}

// Relay names the relay as the operator tells it apart: its host, and its port unless
// that is the scheme's default, without URL encoding.
func (r RouterOS) Relay() string {
	if r.HasDefaultPort() {
		return r.Host
	}

	return net.JoinHostPort(r.Host, r.Port.String())
}

// HasDefaultPort reports whether the relay's port is its scheme's default, which a URL
// leaves out.
func (r RouterOS) HasDefaultPort() bool { return r.Port == r.defaultPort() }

func (RouterOS) relayed() {}

func (RouterOS) method() Method { return MethodRouterOS }

// defaultPort is the API's port for the scheme: 80 for http, else https's 443.
func (r RouterOS) defaultPort() Port {
	if r.Scheme == "http" {
		return PortNumber(defaultRouterOSHTTPPort)
	}

	return PortNumber(defaultRouterOSHTTPSPort)
}

func (r RouterOS) compile(Destination) (Params, error) {
	if r.Host == "" || r.Username == "" || r.Password == "" {
		return nil, errors.New("routeros requires relay, username and password")
	}

	switch r.Scheme {
	case "":
		r.Scheme = defaultRouterOSScheme
	case "http", "https":
	default:
		return nil, fmt.Errorf("invalid scheme %q", r.Scheme)
	}

	host, err := routerOSHost(r.Host)
	if err != nil {
		return nil, err
	}

	port, err := r.Port.resolve(MethodRouterOS, r.defaultPort())
	if err != nil {
		return nil, err
	}

	v, err := r.Verify.resolve(VerifyEnabled)
	if err != nil {
		return nil, err
	}

	r.Host, r.Port, r.Verify = host, port, v

	return r, nil
}

// routerOSHost checks the relay host: an IP literal in its canonical spelling, or a host
// name as written, whose characters can neither end nor escape the host of the API's URL.
func routerOSHost(host string) (string, error) {
	if a, ok := ipLiteral(host); ok {
		ip, err := canonicalIP(host, a)
		if err != nil {
			return "", err
		}

		return ip.String(), nil
	}

	if !isHostName(host) {
		return "", fmt.Errorf(
			"invalid routeros relay host %q: expected a host name or IP address",
			host,
		)
	}

	return host, nil
}

// identity reads the filled-in port, so a relay with its scheme's default port written
// out is the relay without one.
func (r RouterOS) identity(b *strings.Builder) {
	writeKey(b, r.Host)
	writeKey(b, r.Scheme)
	writeKey(b, r.Port.String())
	writeKey(b, r.Verify.resolveSpelling())
}
