package prober

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func loopbackRouterOS(t *testing.T, endpoint string) *routerOSPinger {
	t.Helper()

	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	p, err := New(compiled(t, probe.Spec{
		Addr: "target-only.invalid",
		Params: probe.RouterOS{
			Host: u.Hostname(), Scheme: u.Scheme, Port: probe.PortNumber(port),
			Username: "monitor", Password: "secret",
		},
	}), "row#1")
	if err != nil {
		t.Fatal(err)
	}

	ros, ok := p.(*routerOSPinger)
	if !ok {
		t.Fatalf("unexpected adapter %T", p)
	}

	t.Cleanup(ros.Close)

	return ros
}

// A real relay connection must stop while waiting for headers and while decoding a
// partial JSON body. Returning from Send alone would miss a connection left running.
func TestRouterOSBlockedResponseHonorsCallerLifetime(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		for _, deadline := range []bool{false, true} {
			name := "cancel"
			if deadline {
				name = "deadline"
			}

			t.Run(stage+"/"+name, func(t *testing.T) {
				started := make(chan struct{})
				disconnected := make(chan struct{})
				release := make(chan struct{})
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, err := io.Copy(io.Discard, r.Body)
						if err != nil {
							t.Error(err)

							return
						}

						if stage == "body" {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusOK)

							_, err := w.Write([]byte("["))
							if err != nil {
								t.Error(err)

								return
							}

							err = http.NewResponseController(w).Flush()
							if err != nil {
								t.Error(err)

								return
							}
						}

						close(started)

						select {
						case <-r.Context().Done():
							close(disconnected)
						case <-release:
						}
					}),
				)
				t.Cleanup(server.Close)
				t.Cleanup(func() { close(release) })

				p := loopbackRouterOS(t, server.URL)

				ctx, cancel := context.WithCancel(t.Context())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
				}
				defer cancel()

				results := make(chan probe.Result, 1)
				go func() { results <- p.Send(ctx) }()

				awaitProbeSignal(t, started)

				if !deadline {
					cancel()
				}

				assertUnobservedProbe(t, awaitProbeResult(ctx, t, results), probe.RelayTimeout)
				awaitProbeSignal(t, disconnected)
			})
		}
	}
}

func TestRouterOSLoopbackReusesAndClosesConnections(t *testing.T) {
	const rounds = 12

	var connections atomic.Int32

	closed := make(chan struct{}, rounds)
	server := httptest.NewUnstartedServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/rest/ping" {
				t.Errorf("request = %s %s, want POST /rest/ping", r.Method, r.URL.Path)
			}

			user, pass, ok := r.BasicAuth()
			if !ok || user != "monitor" || pass != "secret" {
				t.Error("compiled credentials did not reach relay")
			}

			var request struct {
				Address string `json:"address"`
				Count   int    `json:"count"`
			}

			err := json.NewDecoder(r.Body).Decode(&request)
			if err != nil {
				t.Error(err)

				return
			}

			if request.Address != "target-only.invalid" || request.Count != 1 {
				t.Errorf("relay ping request = %+v", request)
			}

			w.Header().Set("Content-Type", "application/json")

			_, err = w.Write([]byte(`[{"packet-loss":"0","min-rtt":"1ms500us"}]`))
			if err != nil {
				t.Error(err)
			}
		}),
	)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}

		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	p := loopbackRouterOS(t, server.URL)

	for round := range rounds {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		result := p.Send(ctx)

		cancel()

		if result.Code != probe.Success || result.RTT != 1.5 {
			t.Fatalf("round %d: result = %+v", round, result)
		}
	}

	if got := connections.Load(); got != 1 {
		t.Fatalf("%d probes opened %d connections, want one reused connection", rounds, got)
	}

	p.Close()
	awaitProbeSignal(t, closed)
	p.Close()
}
