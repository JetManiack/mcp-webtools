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
//   - SSRF is blocked: the RoundTripper wrapper resolves the request's
//     target hostname and rejects any address in a private or loopback
//     range before the request reaches the transport layer. Checking the
//     URL here (not in DialContext) means the guard fires even when the
//     TCP connection goes through an HTTP proxy — DialContext only sees
//     the proxy address, not the final destination.
package httpclient

import (
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

// ssrfGuard is a RoundTripper that checks the request's target URL before
// delegating to the inner transport. It rejects requests to private or
// loopback addresses regardless of whether the connection goes through an
// HTTP proxy. Note: a DNS-rebinding window remains between this check and
// the actual connection; squid ACLs (deny to_localnet) provide the second
// layer of defence.
type ssrfGuard struct {
	inner http.RoundTripper
}

func (g *ssrfGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()

	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("ssrf guard: address %s is a private or loopback IP", ip)
		}
		return g.inner.RoundTrip(req)
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(req.Context(), host)
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

	return g.inner.RoundTrip(req)
}

// New returns an *http.Client with retry/backoff, environment proxy
// support, and SSRF protection. Use this for requests to agent-supplied
// URLs. timeout bounds each individual attempt; retryMax is the number of
// retries after the first attempt (negative falls back to 2).
func New(timeout time.Duration, retryMax int) *http.Client {
	return build(timeout, retryMax, true)
}

// NewInternal returns an *http.Client identical to New but without SSRF
// protection. Use this only for requests to operator-configured URLs
// (e.g. a SearXNG instance the operator deploys) where the target address
// is not agent-supplied.
func NewInternal(timeout time.Duration, retryMax int) *http.Client {
	return build(timeout, retryMax, false)
}

func build(timeout time.Duration, retryMax int, ssrf bool) *http.Client {
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

	var rt http.RoundTripper = transport
	if ssrf {
		rt = &ssrfGuard{inner: transport}
	}
	rc.HTTPClient.Transport = rt

	client := rc.StandardClient()
	// One budget for the whole exchange, retries included — the caller's
	// timeout stays the hard bound on how long a request goroutine lives,
	// exactly as it was before retries existed. Retries only help when the
	// failed attempts were fast (resets, refused connections, quick 5xx).
	client.Timeout = timeout
	return client
}
