package httpclient

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewSetsTimeout(t *testing.T) {
	c := New(15*time.Second, 2)
	if c.Timeout != 15*time.Second {
		t.Fatalf("Timeout = %v, want 15s", c.Timeout)
	}
}

// TestProxyFromEnvironment proves requests are routed through the proxy
// named in HTTPS_PROXY/HTTP_PROXY. This is load-bearing: cluster egress
// policy assumes this server's traffic can be forced through a logging
// proxy, and a transport that quietly ignores the variables bypasses it.
func TestProxyFromEnvironment(t *testing.T) {
	var proxied atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	c := New(5*time.Second, 0)
	// http.ProxyFromEnvironment caches nothing per-client, but never proxies
	// localhost — use a non-local name; the proxy answers without dialing it.
	resp, err := c.Get("http://proxy-check.invalid/")
	if err != nil {
		t.Fatalf("Get through proxy: %v", err)
	}
	resp.Body.Close()
	if !proxied.Load() {
		t.Fatal("request did not go through the proxy")
	}
}

// TestRetryOn5xx proves a transient 5xx is retried within the same call.
func TestRetryOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(10*time.Second, 2)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server saw %d calls, want 2 (one failure, one retry)", got)
	}
}

func TestNoProxyHonoured(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1") // unroutable: any proxied request fails
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(5*time.Second, 0)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get with NO_PROXY: %v (request was proxied despite NO_PROXY)", err)
	}
	resp.Body.Close()
}
