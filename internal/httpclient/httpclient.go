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
package httpclient

import (
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// New returns an *http.Client with retry/backoff and environment proxy
// support. timeout bounds each individual attempt; retryMax is the number
// of retries after the first attempt (negative falls back to 2).
func New(timeout time.Duration, retryMax int) *http.Client {
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
	rc.HTTPClient.Transport = transport

	client := rc.StandardClient()
	// One budget for the whole exchange, retries included — the caller's
	// timeout stays the hard bound on how long a request goroutine lives,
	// exactly as it was before retries existed. Retries only help when the
	// failed attempts were fast (resets, refused connections, quick 5xx).
	client.Timeout = timeout
	return client
}
