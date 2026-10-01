package prober

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func newTestRouterOS(endpoint string) *routerOSPinger {
	return &routerOSPinger{
		url:    endpoint,
		user:   "u",
		pass:   "p",
		client: &http.Client{},
		addr:   "1.2.3.4",
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// rosInsecure reports the InsecureSkipVerify the pinger's client was built with.
func rosInsecure(t *testing.T, p *routerOSPinger) bool {
	t.Helper()

	tr, ok := p.client.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatalf("client transport missing TLS config: %#v", p.client.Transport)
	}

	return tr.TLSClientConfig.InsecureSkipVerify
}

func newRouterOSFromParams(t *testing.T, r probe.RouterOS) *routerOSPinger {
	t.Helper()

	if r.Host == "" {
		r.Host = "ros"
	}

	r.Username = "u"
	r.Password = "p"

	p, err := New(compiled(t, probe.Spec{Addr: "1.2.3.4", Params: r}), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	ros, ok := p.(*routerOSPinger)
	if !ok {
		t.Fatalf("unexpected %T", p)
	}

	t.Cleanup(ros.Close)

	return ros
}

// The REST URL names the relay's host, an IPv6 address in brackets with a zone's '%'
// escaped, or url.Parse rejects it ("invalid port") and every probe fails; and its port
// only when that is not the scheme's default.
func TestRouterOSURLBracketsIPv6(t *testing.T) {
	cases := []struct {
		relay probe.RouterOS
		want  string
	}{
		{probe.RouterOS{Host: "2001:db8::1"}, "https://[2001:db8::1]/rest/ping"},
		{probe.RouterOS{Host: "192.0.2.1"}, "https://192.0.2.1/rest/ping"},
		{probe.RouterOS{Host: "ros.example", Scheme: "http"}, "http://ros.example/rest/ping"},
		{
			probe.RouterOS{Host: "ros.example", Port: probe.PortNumber(443)},
			"https://ros.example/rest/ping",
		},
		{probe.RouterOS{Host: "fe80::1%ether1"}, "https://[fe80::1%25ether1]/rest/ping"},
		{probe.RouterOS{Host: "fe80::1%Ethernet 2"}, "https://[fe80::1%25Ethernet%202]/rest/ping"},
		{probe.RouterOS{Host: "::ffff:192.0.2.1"}, "https://192.0.2.1/rest/ping"},
		{
			probe.RouterOS{Host: "2001:0db8::1", Port: probe.PortNumber(80)},
			"https://[2001:db8::1]:80/rest/ping",
		},
		{
			probe.RouterOS{Host: "fe80::1%ether1", Port: probe.PortNumber(8080)},
			"https://[fe80::1%25ether1]:8080/rest/ping",
		},
	}
	for _, c := range cases {
		p := newRouterOSFromParams(t, c.relay)
		if p.url != c.want {
			t.Errorf("relay %+v: url = %q, want %q", c.relay, p.url, c.want)
		}

		_, err := url.Parse(p.url)
		if err != nil {
			t.Errorf("relay %+v: url %q does not parse: %v", c.relay, p.url, err)
		}
	}
}

// verify is a boolean defaulting to secure: only an explicit false turns verification
// off. configfile stores every truthy/falsy spelling as the canonical word, and
// probe.Compile rejects any other value, so no spelling can silently fail open.
func TestRouterOSVerifyBool(t *testing.T) {
	for verify, insecure := range map[probe.Verification]bool{
		probe.VerifyDefault:  false,
		probe.VerifyEnabled:  false,
		probe.VerifyDisabled: true,
	} {
		{
			got := rosInsecure(
				t,
				newRouterOSFromParams(t, probe.RouterOS{Verify: verify}),
			)
			if got != insecure {
				t.Errorf("verify %v: insecure=%v", verify, got)
			}
		}
	}
}

func TestRouterOSSend(t *testing.T) {
	cases := []struct {
		name     string
		body     string // REST response body; written verbatim.
		status   int    // HTTP status; 0 means 200 OK.
		wantCode probe.ResultCode
		wantRTT  float64 // checked only when wantSuccess.
	}{
		{
			name:     "success",
			body:     `[{"packet-loss":"0","min-rtt":"1ms500us","ttl":"58"}]`,
			wantCode: probe.Success,
			wantRTT:  1.5,
		},
		{
			name:     "packet loss",
			body:     `[{"packet-loss":"1","min-rtt":"0us","ttl":"0"}]`,
			wantCode: probe.Failed,
		},
		{
			name:     "100 percent loss",
			body:     `[{"packet-loss":"100","min-rtt":"0us"}]`,
			wantCode: probe.Failed,
		},
		{
			name:     "zero RTT",
			body:     `[{"packet-loss":"0","min-rtt":"0us"}]`,
			wantCode: probe.Success,
		},
		// The router's own failures are the relay's (s), never the target's X: a
		// refusal, an error status, and a reply that is no ping result.
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: probe.RelayFailed},
		{name: "http error", status: http.StatusInternalServerError, wantCode: probe.RelayFailed},
		{name: "not json", body: `<html>`, wantCode: probe.RelayFailed},
		{name: "empty array", body: `[]`, wantCode: probe.RelayFailed},
		{
			name:     "negative loss",
			body:     `[{"packet-loss":"-1","min-rtt":"1ms"}]`,
			wantCode: probe.RelayFailed,
		},
		{
			name:     "non-numeric loss",
			body:     `[{"packet-loss":"invalid","min-rtt":"1ms"}]`,
			wantCode: probe.RelayFailed,
		},
		{
			name:     "missing loss",
			body:     `[{"min-rtt":"1ms"}]`,
			wantCode: probe.RelayFailed,
		},
		{
			name:     "excess loss",
			body:     `[{"packet-loss":"101","min-rtt":"1ms"}]`,
			wantCode: probe.RelayFailed,
		},
		{
			name:     "missing rtt",
			body:     `[{"packet-loss":"0","min-rtt":"garbage"}]`,
			wantCode: probe.RelayFailed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestRouterOS("http://router.example/rest/ping")
			p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if c.status != 0 {
						w.WriteHeader(c.status)
					}

					_, _ = w.Write([]byte(c.body))
				}).ServeHTTP(w, r)

				return w.Result(), nil
			})

			res := p.Send(t.Context())
			if res.Code != c.wantCode {
				t.Fatalf("Code = %v, want %v (%+v)", res.Code, c.wantCode, res)
			}

			if c.wantCode != probe.Success {
				return
			}

			if !(math.Abs(res.RTT-c.wantRTT) <= 1e-9) {
				t.Errorf("RTT = %v, want %v", res.RTT, c.wantRTT)
			}
		})
	}
}

func TestParseRouterOSMinRTT(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"1ms500us", 1.5, true},
		{"500us", 0.5, true},
		{"2ms0us", 2.0, true},
		{"10ms250us", 10.25, true},
		// Whole-ms / seconds shapes the old mandatory-"us" regex dropped to (0,false).
		{"5ms", 5, true},
		{"2s", 2000, true},
		{"1s500ms", 1500, true},
		{"0us", 0, true},
		{"1us", 0.001, true},
		{"", 0, false},
		{"-1ms", 0, false},
		{"garbage", 0, false},
		{"garbage1ms", 0, false},
		{"1msgarbage", 0, false},
		{strings.Repeat("9", 400) + "ms", 0, false},
		{"1" + strings.Repeat("0", 306) + "s", 0, false},
	}
	for _, c := range cases {
		got, ok := parseRouterOSMinRTT(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.in, ok, c.ok)

			continue
		}

		if ok && !(math.Abs(got-c.want) <= 1e-9) {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}

// A router that cannot be reached is the relay failing too: t when it does not answer in
// time, s when the connection is refused.
func TestRouterOSUnreachableIsTheRelays(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want probe.ResultCode
	}{
		{"refused", syscall.ECONNREFUSED, probe.RelayFailed},
		{"timeout", context.DeadlineExceeded, probe.RelayTimeout},
	} {
		p := newTestRouterOS("http://router.example/rest/ping")
		p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, c.err
		})

		if res := p.Send(t.Context()); res.Code != c.want {
			t.Errorf("%s: Code = %v, want %v", c.name, res.Code, c.want)
		}
	}
}
