package ping

import (
	"context"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRouterOS(url string) *routerOSPinger {
	return &routerOSPinger{url: url, user: "u", pass: "p", client: &http.Client{}, addr: "1.2.3.4"}
}

// rosInsecure reports the InsecureSkipVerify the pinger's client was built with.
func rosInsecure(t *testing.T, p *routerOSPinger) bool {
	t.Helper()

	tr, ok := p.client.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatalf("client transport missing TLS config: %#v", p.client.Transport)
	}

	return tr.TLSClientConfig.InsecureSkipVerify
}

func newRouterOSFromRelay(t *testing.T, relay map[string]string) *routerOSPinger {
	t.Helper()

	base := map[string]string{"relay": "ros", "username": "u", "password": "p"}
	maps.Copy(base, relay)

	p, err := newRouterOSPinger(Spec{Addr: "1.2.3.4", Relay: base})
	if err != nil {
		t.Fatalf("newRouterOSPinger(%v) error: %v", relay, err)
	}

	ros, ok := p.(*routerOSPinger)
	if !ok {
		t.Fatalf("newRouterOSPinger returned %T, want *routerOSPinger", p)
	}

	return ros
}

// An IPv6 management address must be bracketed in the REST URL, or url.Parse
// rejects it ("invalid port") and every probe fails. A hostname / IPv4 / explicit
// host:port pass through unchanged.
func TestRouterOSURLBracketsIPv6(t *testing.T) {
	cases := []struct {
		relay string
		want  string
	}{
		{"2001:db8::1", "https://[2001:db8::1]/rest/ping"},
		{"192.0.2.1", "https://192.0.2.1/rest/ping"},
		{"ros.example", "https://ros.example/rest/ping"},
		{"[2001:db8::1]", "https://[2001:db8::1]/rest/ping"},
	}
	for _, c := range cases {
		p := newRouterOSFromRelay(t, map[string]string{"relay": c.relay})
		if p.url != c.want {
			t.Errorf("relay %q: url = %q, want %q", c.relay, p.url, c.want)
		}
	}
}

// verify is a lenient boolean defaulting to secure: only an explicit falsy value
// turns verification off. The previous "anything but the literal true" rule made
// verify=yes/1/on silently insecure.
func TestRouterOSVerifyBool(t *testing.T) {
	cases := []struct {
		verify       string
		set          bool
		wantInsecure bool
	}{
		{set: false, wantInsecure: false}, // unset -> secure default.
		{verify: "true", set: true, wantInsecure: false},
		{verify: "yes", set: true, wantInsecure: false}, // was wrongly insecure before.
		{verify: "1", set: true, wantInsecure: false},
		{verify: "on", set: true, wantInsecure: false},
		{verify: "false", set: true, wantInsecure: true},
		{verify: "no", set: true, wantInsecure: true},
		{verify: "0", set: true, wantInsecure: true},
		{verify: "garbage", set: true, wantInsecure: false}, // unknown -> secure default.
	}
	for _, c := range cases {
		relay := map[string]string{}
		if c.set {
			relay["verify"] = c.verify
		}

		if got := rosInsecure(t, newRouterOSFromRelay(t, relay)); got != c.wantInsecure {
			t.Errorf(
				"verify=%q (set=%v): insecure = %v, want %v",
				c.verify,
				c.set,
				got,
				c.wantInsecure,
			)
		}
	}
}

func TestRouterOSSend(t *testing.T) {
	cases := []struct {
		name        string
		body        string // REST response body; written verbatim.
		status      int    // HTTP status; 0 means 200 OK.
		wantSuccess bool
		wantRTT     float64 // checked only when wantSuccess.
		wantTTL     int     // checked only when wantSuccess.
	}{
		{
			name:        "success",
			body:        `[{"packet-loss":"0","min-rtt":"1ms500us","ttl":"58"}]`,
			wantSuccess: true,
			wantRTT:     1.5,
			wantTTL:     58,
		},
		{name: "packet loss", body: `[{"packet-loss":"1","min-rtt":"0us","ttl":"0"}]`},
		{name: "http error", status: http.StatusInternalServerError},
		{name: "empty array", body: `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if c.status != 0 {
						w.WriteHeader(c.status)
					}

					_, _ = w.Write([]byte(c.body))
				}),
			)
			defer srv.Close()

			res := newTestRouterOS(srv.URL).Send(context.Background())
			if res.Success != c.wantSuccess {
				t.Fatalf("Success = %v, want %v (%+v)", res.Success, c.wantSuccess, res)
			}

			if !c.wantSuccess {
				return
			}

			if math.Abs(res.RTT-c.wantRTT) > 1e-9 {
				t.Errorf("RTT = %v, want %v", res.RTT, c.wantRTT)
			}

			if res.TTL != c.wantTTL {
				t.Errorf("TTL = %d, want %d", res.TTL, c.wantTTL)
			}
		})
	}
}
