// Package fetch retrieves the content of a URL on behalf of an agent.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout and DefaultMaxBytes are used when New is given
// non-positive values.
const (
	DefaultTimeout  = 15 * time.Second
	DefaultMaxBytes = 2 << 20 // 2 MiB
)

var ErrEmptyURL = errors.New("url must not be empty")

// Result is one fetched document.
type Result struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type,omitempty"`
	Content     string `json:"content"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
}

// Fetcher performs HTTP GETs with a bounded timeout and a bounded response
// size.
type Fetcher struct {
	client   *http.Client
	maxBytes int64
}

// New returns a Fetcher. A non-positive timeout or maxBytes falls back to
// the package defaults — an unbounded HTTP client is never an acceptable
// configuration here, since the URL comes from an agent and a slow or
// enormous response would otherwise pin a request goroutine and its memory
// indefinitely.
func New(timeout time.Duration, maxBytes int64) *Fetcher {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Fetcher{
		client:   &http.Client{Timeout: timeout},
		maxBytes: maxBytes,
	}
}

// Fetch GETs rawURL and returns its body, truncated to the Fetcher's
// maxBytes. A non-2xx status is an error: an agent asking for a page's
// content is not served by handing it an error page's body as if it were
// the answer.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, ErrEmptyURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	// Explicit scheme check rather than relying on the transport to refuse:
	// it keeps the error message useful, and keeps the tool from becoming a
	// way to reach schemes the http.Client might support later.
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme %q: only http and https are allowed", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", parsed.Redacted(), resp.Status)
	}

	// Read one byte past the limit so a body landing exactly on maxBytes
	// isn't reported as truncated.
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	truncated := int64(len(body)) > f.maxBytes
	if truncated {
		body = body[:f.maxBytes]
	}

	return &Result{
		URL:         parsed.Redacted(),
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Content:     string(body),
		Bytes:       int64(len(body)),
		Truncated:   truncated,
	}, nil
}
