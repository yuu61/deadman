package probe

import "testing"

func compileRouterOS(host string, port Port, scheme string) (Plan, error) {
	return Compile(Spec{
		Addr:   "192.0.2.1",
		Params: RouterOS{Host: host, Port: port, Scheme: scheme, Username: "u", Password: "p"},
	})
}

// The relay's host goes into the API's URL, so a name holding a character that would end
// or escape the URL's host is refused, as is a port out of range. Splitting relay= into
// the two is the config syntax's job.
func TestRouterOSRelayIsValidated(t *testing.T) {
	for _, c := range []struct {
		host string
		port Port
	}{
		{"router", PortNumber(0)},
		{"router", PortNumber(65536)},
		{"router:8443", Port{}},
		{"[2001:db8::1]", Port{}},
		{"router/path", Port{}},
		{"user@router", Port{}},
		{"router?query", Port{}},
		{"router#fragment", Port{}},
		{"::ffff:192.0.2.1%eth0", Port{}},
		// net/url refuses these in a host, so each would fail every probe.
		{"router|1", Port{}},
		{"a{b", Port{}},
		{"router^", Port{}},
		{"router`", Port{}},
		{"router\x01", Port{}},
	} {
		_, err := compileRouterOS(c.host, c.port, "")
		if err == nil {
			t.Errorf("invalid relay %q port %v compiled", c.host, c.port)
		}
	}
}

// Compile fills the scheme's default port, so a relay with that port written out is the
// same relay; an IP literal is spelled canonically, and a name is kept as written.
func TestRouterOSRelayIsCanonical(t *testing.T) {
	type relay struct {
		host   string
		port   Port
		scheme string
	}

	for _, pair := range [][2]relay{
		{{"2001:db8::1", Port{}, ""}, {"2001:0db8:0:0:0:0:0:1", Port{}, ""}},
		{{"::ffff:192.0.2.9", Port{}, ""}, {"192.0.2.9", Port{}, ""}},
		{{"router.example", PortNumber(443), ""}, {"router.example", Port{}, "https"}},
		{{"router.example", PortNumber(80), "http"}, {"router.example", Port{}, "http"}},
	} {
		a, err := compileRouterOS(pair[0].host, pair[0].port, pair[0].scheme)
		if err != nil {
			t.Fatal(err)
		}

		b, err := compileRouterOS(pair[1].host, pair[1].port, pair[1].scheme)
		if err != nil {
			t.Fatal(err)
		}

		if a.Identity() != b.Identity() {
			t.Errorf("equivalent relays %+v and %+v have distinct identities", pair[0], pair[1])
		}
	}

	for _, pair := range [][2]relay{
		{{"router.example", Port{}, "http"}, {"router.example", Port{}, "https"}},
		{{"router.example", PortNumber(8443), ""}, {"router.example", Port{}, ""}},
		{{"ROUTER.example", Port{}, ""}, {"router.example", Port{}, ""}},
	} {
		a, errA := compileRouterOS(pair[0].host, pair[0].port, pair[0].scheme)
		b, errB := compileRouterOS(pair[1].host, pair[1].port, pair[1].scheme)

		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}

		if a.Identity() == b.Identity() {
			t.Errorf("distinct relays %+v and %+v share an identity", pair[0], pair[1])
		}
	}
}

// Relay shows the port only where it is not the scheme's default.
func TestRouterOSRelayLabel(t *testing.T) {
	for _, c := range []struct {
		host   string
		port   Port
		scheme string
		want   string
	}{
		{"r", Port{}, "", "r"},
		{"r", PortNumber(443), "https", "r"},
		{"r", PortNumber(80), "http", "r"},
		{"r", PortNumber(443), "http", "r:443"},
		{"2001:db8::1", PortNumber(8443), "", "[2001:db8::1]:8443"},
	} {
		plan, err := compileRouterOS(c.host, c.port, c.scheme)
		if err != nil {
			t.Fatal(err)
		}

		r, ok := plan.Params().(RouterOS)
		if !ok {
			t.Fatalf("compiled %T", plan.Params())
		}

		if got := r.Relay(); got != c.want {
			t.Errorf("%+v: Relay() = %q, want %q", c, got, c.want)
		}
	}
}
