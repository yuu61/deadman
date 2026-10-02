package prober

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// addPlanSeeds adds the spellings earlier fixes were about: zones, IPv4-mapped literals,
// user@host relays, a port or brackets in a relay, spaces, shell syntax and option-like
// operands. Each seed is the destination, the relay, a namespace name, and two values
// that fill the credentials and the source interface.
func addPlanSeeds(f *testing.F) {
	f.Helper()

	for _, seed := range [][5]string{
		{"192.0.2.1", "jump", "ns", "ops", "/k"},
		{"example.com", "ops@jump", "blue", "", ""},
		{"fe80::1%eth0", "fe80::2%eth0", "ns", "eth0", "secret"},
		{"::ffff:192.0.2.1", "::ffff:192.0.2.9", "ns", "eth1", "k"},
		{"2001:db8::1", "[2001:db8::2]", "v", "u", "p"},
		{"example.com", "r1:8443", "ns", "u", "p"},
		{"web 1", "router.example", "n s", "u v", "p q"},
		{"h*", "$(reboot)", "ns", "it's", "`id`"},
		{"-8", "-oProxyCommand=/tmp/x", "-x", "-l", "-i"},
		{"192.0.2.1", "ops@admin@jump", "ns", "u", "p"},
		{"192.0.2.1", "router_1.example", "ns", "u", "p"},
	} {
		f.Add(seed[0], seed[1], seed[2], seed[3], seed[4])
	}
}

// buildableSpecs spells the modes whose adapters do no I/O when built over the fuzzed
// values. The relayed ones are tried with and without a resolve family, so both literals
// and host names reach Compile.
func buildableSpecs(addr, relay, name, value, secret string) []probe.Spec {
	var src probe.Source
	if value != "" {
		src = probe.SourceInterface(value)
	}

	families := []probe.Family{probe.FamilyUnknown, probe.FamilyIPv4, probe.FamilyIPv6}

	specs := make([]probe.Spec, 0, 1+4*len(families))
	specs = append(
		specs,
		probe.Spec{
			Addr:   addr,
			Params: probe.RouterOS{Host: relay, Username: value, Password: secret},
		},
	)

	for _, family := range families {
		specs = append(specs,
			probe.Spec{Addr: addr, Params: probe.TCP{Port: probe.PortNumber(80), Family: family}},
			probe.Spec{Addr: addr, Params: probe.Netns{Name: name, Source: src, Family: family}},
			probe.Spec{Addr: addr, Params: probe.VRF{Name: name, Family: family}},
			probe.Spec{
				Addr: addr,
				Params: probe.SSH{
					Host: relay, OS: probe.OSLinux, Family: family, User: value, Key: secret,
				},
			},
		)
	}

	return specs
}

// operands are the compiled values a mode puts in its argv as bare operands, where a
// leading '-' would read as an option. Building such a plan is the one refusal the
// adapters add to Compile's.
func operands(plan probe.Plan) []string {
	dest := plan.Destination().String()

	switch p := plan.Params().(type) {
	case probe.Netns:
		return []string{p.Name, dest}
	case probe.VRF:
		return []string{p.Name, dest}
	case probe.SSH:
		return []string{p.Host, dest}
	case probe.Direct, probe.TCP, probe.QUIC, probe.Nexthop, probe.SNMP, probe.RouterOS:
		return nil // sent through sockets, or as a value of a preceding option.
	default:
		return nil
	}
}

// A plan Compile accepts builds, and what its adapter built can be sent every round: no
// argv value holds a NUL, which no exec can pass, and the RouterOS URL is one a request
// can be made of, naming the compiled host and port. The only refusal left to the
// adapters is an option-like operand.
func FuzzCompiledPlansBuild(f *testing.F) {
	addPlanSeeds(f)

	f.Fuzz(func(t *testing.T, addr, relay, name, value, secret string) {
		for _, spec := range buildableSpecs(addr, relay, name, value, secret) {
			plan, err := probe.Compile(spec)
			if err != nil {
				continue
			}

			optionLike := slices.ContainsFunc(operands(plan), func(v string) bool {
				return strings.HasPrefix(v, "-")
			})

			p, err := New(plan, "row#1")
			if optionLike {
				if err == nil {
					t.Fatalf("%s: option-like operand built: %+v", plan.Method(), plan.Params())
				}

				continue
			}

			if err != nil {
				t.Fatalf(
					"%s: compiled plan %q did not build: %v",
					plan.Method(),
					plan.Identity(),
					err,
				)
			}

			checkSendable(t, plan, p)

			if closer, ok := p.(interface{ Close() }); ok {
				closer.Close()
			}
		}
	})
}

func checkSendable(t *testing.T, plan probe.Plan, p probe.Pinger) {
	t.Helper()

	switch built := p.(type) {
	case *subprocessPinger:
		args := built.buildArgs()
		checkArgv(t, plan, args)

		if !built.ssh && args[len(args)-1] != plan.Destination().String() {
			t.Fatalf("%s: argv %q does not end with the destination", plan.Method(), args)
		}
	case *tcpPinger:
		if built.dest != plan.Destination() {
			t.Fatalf("tcp destination %v differs from plan %v", built.dest, plan.Destination())
		}
	case *routerOSPinger:
		checkRouterOSURL(t, plan, built.url)
	default:
		t.Fatalf("%s built %T", plan.Method(), p)
	}
}

func checkArgv(t *testing.T, plan probe.Plan, args []string) {
	t.Helper()

	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			t.Fatalf("%s: argv value %q holds a NUL: %+v", plan.Method(), arg, plan.Params())
		}
	}
}

func checkRouterOSURL(t *testing.T, plan probe.Plan, url string) {
	t.Helper()

	r, ok := plan.Params().(probe.RouterOS)
	if !ok {
		t.Fatalf("routeros pinger built from %T", plan.Params())
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, http.NoBody)
	if err != nil {
		t.Fatalf("relay %q: no request can be made of %q: %v", r.Host, url, err)
	}

	wantPort := r.Port.String()
	if r.HasDefaultPort() {
		wantPort = ""
	}

	if req.URL.Hostname() != r.Host || req.URL.Port() != wantPort {
		t.Fatalf("relay %q port %s: URL %q names %q port %q", r.Host, r.Port, url,
			req.URL.Hostname(), req.URL.Port())
	}
}
