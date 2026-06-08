package ping

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRouterOS(url string) *routerOSPinger {
	return &routerOSPinger{url: url, user: "u", pass: "p", insecure: true, addr: "1.2.3.4"}
}

func TestRouterOSSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"packet-loss":"0","min-rtt":"1ms500us","ttl":"58"}]`))
	}))
	defer srv.Close()

	res := newTestRouterOS(srv.URL).Send(context.Background())
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}

	if math.Abs(res.RTT-1.5) > 1e-9 {
		t.Errorf("RTT = %v, want 1.5", res.RTT)
	}

	if res.TTL != 58 {
		t.Errorf("TTL = %d, want 58", res.TTL)
	}
}

func TestRouterOSPacketLoss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"packet-loss":"1","min-rtt":"0us","ttl":"0"}]`))
	}))
	defer srv.Close()

	if res := newTestRouterOS(srv.URL).Send(context.Background()); res.Success {
		t.Fatalf("expected failure on packet loss, got %+v", res)
	}
}

func TestRouterOSHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if res := newTestRouterOS(srv.URL).Send(context.Background()); res.Success {
		t.Fatalf("expected failure on 500, got %+v", res)
	}
}

func TestRouterOSEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if res := newTestRouterOS(srv.URL).Send(context.Background()); res.Success {
		t.Fatalf("expected failure on empty array, got %+v", res)
	}
}
