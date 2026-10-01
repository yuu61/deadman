package prober

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// routerOSPinger pings via the RouterOS REST API (POST /rest/ping). It uses only
// the standard library. RouterOS returns its fields as strings. The http.Client is
// built once and reused across every probe: a fresh client/transport per Send
// leaks the idle keep-alive connection (and its goroutines) into a discarded
// transport each round.
//
// The router is the relay: when it cannot be reached, refuses the request or answers
// with anything but a ping result, the probe observed nothing about the target and
// reports the relay's failure (t when it did not answer in time, s otherwise), as the
// ssh and snmp relays do.
type routerOSPinger struct {
	url    string
	user   string
	pass   string
	client *http.Client
	addr   string
}

const routerOSTimeout = 5 * time.Second

// routerOSIdleTimeout bounds how long a keep-alive connection to the router idles, as
// http.DefaultTransport's does. Close already closes the idle ones and every one that
// goes idle after it; this also bounds a connection nothing will close otherwise.
const routerOSIdleTimeout = 90 * time.Second

// maxRouterOSBody caps the REST response body we read, so a hostile or MITM'd endpoint
// (verify=off disables TLS verification) cannot stream an unbounded body into memory. A
// RouterOS ping response is a few hundred bytes; 1 MiB is generous.
const maxRouterOSBody = 1 << 20 // 1 MiB.

// routerOSResp mirrors the RouterOS REST API ping response. The API returns
// kebab-case JSON keys, so the tags must match them verbatim.
type routerOSResp struct {
	PacketLoss string `json:"packet-loss"`
	MinRTT     string `json:"min-rtt"`
}

func newRouterOSPinger(dest probe.Destination, r probe.RouterOS) (probe.Pinger, error) {
	// The router originates the probe. Compile has resolved the scheme and verification.
	client := &http.Client{
		Transport: &http.Transport{
			// #nosec G402 -- self-signed certs are common on network gear; TLS
			// verification is opt-out via the per-target "verify" config key.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: r.Verify != probe.VerifyEnabled},
			IdleConnTimeout: routerOSIdleTimeout,
		},
	}

	return &routerOSPinger{
		url:    (&url.URL{Scheme: r.Scheme, Host: routerOSAuthority(r), Path: "/rest/ping"}).String(),
		user:   r.Username,
		pass:   r.Password,
		client: client,
		addr:   dest.String(),
	}, nil
}

// routerOSAuthority is the URL authority of the router's API: its host, an IPv6 address
// in brackets with an unescaped zone for url.URL to encode (RFC 6874).
// The port is included unless it is the scheme's default.
func routerOSAuthority(r probe.RouterOS) string {
	port := r.Port.String()
	authority := net.JoinHostPort(r.Host, port)

	if r.HasDefaultPort() {
		return strings.TrimSuffix(authority, ":"+port)
	}

	return authority
}

// Close releases pooled connections when the monitoring session is retired. A Send still
// in flight then closes its connection instead of pooling it: CloseIdleConnections also
// closes every connection that goes idle after it, until a new request is made.
func (p *routerOSPinger) Close() { p.client.CloseIdleConnections() }

func (p *routerOSPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, routerOSTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{"address": p.addr, "count": 1})
	if err != nil {
		return probe.UnavailableResult()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return probe.UnavailableResult()
	}

	req.SetBasicAuth(p.user, p.pass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return routerOSUnanswered(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return probe.RelayFailedResult()
	}

	var arr []routerOSResp

	err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, maxRouterOSBody)).Decode(&arr)
	if err != nil {
		return routerOSUnanswered(ctx, err)
	}

	if len(arr) == 0 {
		return probe.RelayFailedResult()
	}

	return routerOSResult(arr[0])
}

// routerOSUnanswered classifies a request the router did not answer: t when it ran out
// of time, s when it could not be reached, refused or broke the exchange.
func routerOSUnanswered(ctx context.Context, err error) probe.Result {
	var ne net.Error
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &ne) && ne.Timeout() {
		return probe.RelayTimeoutResult()
	}

	return probe.RelayFailedResult()
}

// routerOSResult turns one REST reply into a probe result. Only a valid positive
// packet-loss value establishes that the target failed to answer; a reply the router
// spelled wrong is its failure, not the target's.
func routerOSResult(r routerOSResp) probe.Result {
	pl, perr := strconv.Atoi(r.PacketLoss)
	if perr != nil {
		return probe.RelayFailedResult()
	}

	if pl < 0 || pl > 100 {
		return probe.RelayFailedResult()
	}

	if pl > 0 {
		return probe.FailedResult()
	}

	rtt, ok := parseRouterOSMinRTT(r.MinRTT)
	if !ok {
		return probe.RelayFailedResult()
	}

	return probe.SuccessResult(rtt)
}

// msPerS converts seconds to milliseconds.
const msPerS = 1000.0

// reRouterOSUnit matches one "<n><unit>" run of a RouterOS duration. Each unit is
// independently optional and summed, so any combination ("1ms500us", "500us", "5ms",
// "2s") parses — unlike a fixed ms+us shape that dropped whole-ms values.
var reRouterOSUnit = regexp.MustCompile(`(\d+)(us|ms|s)`)

// parseRouterOSMinRTT parses a RouterOS min-rtt duration into milliseconds. Each
// unit run (s/ms/us) is summed, so "1ms500us" -> 1.5, "500us" -> 0.5, "5ms" -> 5
// and "2s" -> 2000 all parse. ok is false when no recognizable unit is present.
func parseRouterOSMinRTT(text string) (float64, bool) {
	matches := reRouterOSUnit.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return 0, false
	}

	var ms float64

	end := 0

	for _, m := range matches {
		if m[0] != end {
			return 0, false
		}

		n, err := strconv.ParseFloat(text[m[2]:m[3]], 64)
		if err != nil {
			return 0, false
		}

		ms += routerOSDurationUnit(n, text[m[4]:m[5]])

		end = m[1]
	}

	return ms, end == len(text) && !math.IsInf(ms, 0) && !math.IsNaN(ms)
}

func routerOSDurationUnit(n float64, unit string) float64 {
	switch unit {
	case "s":
		return n * msPerS
	case "ms":
		return n
	case "us":
		return n / usPerMs
	default:
		return 0 // Unreachable: reRouterOSUnit only captures s/ms/us.
	}
}
