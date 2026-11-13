// Package httpclient builds the outbound HTTP client every tool in this
// server uses. It exists so two behaviours hold everywhere, not just in
// whichever tool remembered them:
//
//   - HTTP(S)_PROXY / NO_PROXY are honoured. The default transport does
//     this too, but an explicit Proxy field survives any future
//     customization of the transport (cluster egress policy routes this
//     server's traffic through a logging proxy, so silently losing proxy
//     support is an incident, not a nuisance).
//   - Transient failures are retried with backoff (hashicorp
//     go-retryablehttp): connection resets and 5xx/429 responses, with the
//     caller's timeout still bounding the total attempt.
//   - SSRF is blocked: the dial step resolves the hostname and rejects any
//     address that falls in a private or loopback range before the TCP
//     connection is opened. Using the resolved IP for the actual dial also
//     prevents DNS-rebinding attacks.
package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// privateRanges lists IP ranges that agents must not be able to reach.
var privateRanges []net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	} {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			privateRanges = append(privateRanges, *ipNet)
		}
	}
}

func isPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

var baseDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
}

// safeDialContext resolves addr, checks that no returned IP is private or
// loopback, then dials the first resolved address directly (bypassing DNS a
// second time) to close the DNS-rebinding window.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: bad addr %q: %w", addr, err)
	}

	// Literal IP — no DNS lookup needed.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("ssrf guard: address %s is a private or loopback IP", ip)
		}
		return baseDialer.DialContext(ctx, network, addr)
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("ssrf guard: no addresses for %q", host)
	}
	for _, a := range addrs {
		if isPrivateIP(a.IP) {
			return nil, fmt.Errorf("ssrf guard: %q resolves to private address %s", host, a.IP)
		}
	}

	// Dial to the first resolved IP directly — not the hostname — so a
	// second DNS lookup (which could return a different address) never happens.
	return baseDialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].IP.String(), port))
}

// New returns an *http.Client with retry/backoff, environment proxy
// support, and SSRF protection. Use this for requests to agent-supplied
// URLs. timeout bounds each individual attempt; retryMax is the number of
// retries after the first attempt (negative falls back to 2).
func New(timeout time.Duration, retryMax int) *http.Client {
	return build(timeout, retryMax, safeDialContext)
}

// NewInternal returns an *http.Client identical to New but without SSRF
// protection. Use this only for requests to operator-configured URLs
// (e.g. a SearXNG instance the operator deploys) where the target address
// is not agent-supplied.
func NewInternal(timeout time.Duration, retryMax int) *http.Client {
	return build(timeout, retryMax, nil)
}

func build(timeout time.Duration, retryMax int, dialCtx func(context.Context, string, string) (net.Conn, error)) *http.Client {
	if retryMax < 0 {
		retryMax = 2
	}

	rc := retryablehttp.NewClient()
	rc.RetryMax = retryMax
	rc.RetryWaitMin = 500 * time.Millisecond
	rc.RetryWaitMax = 5 * time.Second
	rc.Logger = nil

	transport := rc.HTTPClient.Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if dialCtx != nil {
		transport.DialContext = dialCtx
	}
	rc.HTTPClient.Transport = transport

	client := rc.StandardClient()
	// One budget for the whole exchange, retries included — the caller's
	// timeout stays the hard bound on how long a request goroutine lives,
	// exactly as it was before retries existed. Retries only help when the
	// failed attempts were fast (resets, refused connections, quick 5xx).
	client.Timeout = timeout
	return client
}
