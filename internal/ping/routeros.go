package ping

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// routerOSPinger pings via the RouterOS REST API (POST /rest/ping). It uses only
// the standard library. RouterOS returns its fields as strings. The http.Client is
// built once and reused across every probe: a fresh client/transport per Send
// leaks the idle keep-alive connection (and its goroutines) into a discarded
// transport each round.
type routerOSPinger struct {
	url    string
	user   string
	pass   string
	client *http.Client
	addr   string
}

const routerOSTimeout = 5 * time.Second

// maxRouterOSBody caps the REST response body we read, so a hostile or MITM'd endpoint
// (verify=off disables TLS verification) cannot stream an unbounded body into memory. A
// RouterOS ping response is a few hundred bytes; 1 MiB is generous.
const maxRouterOSBody = 1 << 20 // 1 MiB.

// routerOSResp mirrors the RouterOS REST API ping response. The API returns
// kebab-case JSON keys, so the tags must match them verbatim.
type routerOSResp struct {
	PacketLoss string `json:"packet-loss"` //nolint:tagliatelle // RouterOS API key is kebab-case
	MinRTT     string `json:"min-rtt"`     //nolint:tagliatelle // RouterOS API key is kebab-case
	TTL        string `json:"ttl"`
}

func newRouterOSPinger(s Spec) (Pinger, error) {
	if s.Relay["username"] == "" || s.Relay["password"] == "" {
		return nil, fmt.Errorf("'username' and 'password' is required for %s", s.Addr)
	}

	if s.Relay["relay"] == "" {
		return nil, fmt.Errorf("'relay' is not specified for %s", s.Addr)
	}

	// A source= is silently ignored here (the RouterOS box originates the probe); the
	// TUI surfaces a startup warning via ping.SourceUnsupported rather than failing.
	method := s.Relay["method"]
	if method == "" {
		method = "https"
	}

	// verify defaults to true (secure). Only an explicit falsy value (the same
	// spellings the "columns" directive accepts) disables TLS verification; an absent
	// or unrecognized value keeps the secure default rather than silently failing open
	// — the bug the old "anything but the literal true" rule had (verify=yes -> insecure).
	insecure := false

	switch strings.ToLower(s.Relay["verify"]) {
	case "off", "false", "no", "0":
		insecure = true
	default:
		// "", "on", "true", "yes", "1", or any unrecognized value: stay secure.
	}

	client := &http.Client{
		Transport: &http.Transport{
			// #nosec G402 -- self-signed certs are common on network gear; TLS
			// verification is opt-out via the per-target "verify" config key.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
		},
	}

	return &routerOSPinger{
		url:    fmt.Sprintf("%s://%s/rest/ping", method, bracketHost(s.Relay["relay"])),
		user:   s.Relay["username"],
		pass:   s.Relay["password"],
		client: client,
		addr:   s.Addr,
	}, nil
}

// bracketHost wraps a bare IPv6 literal in [] so it forms a valid URL authority; a
// hostname, an IPv4 literal, an already-bracketed literal, or a host:port pass
// through unchanged. Without this an IPv6 RouterOS management address makes
// url.Parse fail ("invalid port"), so every probe returns Failed.
func bracketHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + host + "]"
	}

	return host
}

func (p *routerOSPinger) Send(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, routerOSTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{"address": p.addr, "count": 1})
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}

	req.SetBasicAuth(p.user, p.pass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return Result{Code: Failed, TTL: -1}
	}

	var arr []routerOSResp

	err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, maxRouterOSBody)).Decode(&arr)
	if err != nil || len(arr) == 0 {
		return Result{Code: Failed, TTL: -1}
	}

	r := arr[0]
	// Fail closed: treat an absent/empty/non-numeric packet-loss as a failure rather
	// than reporting the host up on a malformed response.
	pl, perr := strconv.Atoi(r.PacketLoss)
	if perr != nil || pl > 0 {
		return Result{Code: Failed, TTL: -1}
	}

	rtt, _ := ParseRouterOSMinRTT(r.MinRTT)
	ttl, _ := strconv.Atoi(r.TTL)

	return Result{Success: true, Code: Success, RTT: rtt, TTL: ttl}
}
