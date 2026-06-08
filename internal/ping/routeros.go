package ping

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// routerOSPinger pings via the RouterOS REST API (POST /rest/ping). It uses only
// the standard library. RouterOS returns its fields as strings.
type routerOSPinger struct {
	url      string
	user     string
	pass     string
	insecure bool
	addr     string
}

const routerOSTimeout = 5 * time.Second

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

	method := s.Relay["method"]
	if method == "" {
		method = "https"
	}
	// verify defaults to true (secure). insecure = !verify.
	insecure := false
	if v, ok := s.Relay["verify"]; ok {
		insecure = !strings.EqualFold(v, "true")
	}

	return &routerOSPinger{
		url:      fmt.Sprintf("%s://%s/rest/ping", method, s.Relay["relay"]),
		user:     s.Relay["username"],
		pass:     s.Relay["password"],
		insecure: insecure,
		addr:     s.Addr,
	}, nil
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

	client := &http.Client{
		Transport: &http.Transport{
			// #nosec G402 -- self-signed certs are common on network gear; TLS
			// verification is opt-out via the per-target "verify" config key.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: p.insecure},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{Code: Failed, TTL: -1}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return Result{Code: Failed, TTL: -1}
	}

	var arr []routerOSResp

	err = json.NewDecoder(resp.Body).Decode(&arr)
	if err != nil || len(arr) == 0 {
		return Result{Code: Failed, TTL: -1}
	}

	r := arr[0]
	if pl, _ := strconv.Atoi(r.PacketLoss); pl > 0 {
		return Result{Code: Failed, TTL: -1}
	}

	rtt, _ := ParseRouterOSMinRTT(r.MinRTT)
	ttl, _ := strconv.Atoi(r.TTL)

	return Result{Success: true, Code: Success, RTT: rtt, TTL: ttl}
}
