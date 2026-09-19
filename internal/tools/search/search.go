// Package search queries a SearXNG instance on behalf of an agent.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JetManiack/mcp-webtools/internal/cache"
)

// DefaultTimeout is used when New is given a non-positive timeout.
//
// It is deliberately generous: a SearXNG instance fans a query out to
// several upstream engines before answering, so a tight client timeout
// fails against healthy instances rather than protecting against unhealthy
// ones.
const DefaultTimeout = 10 * time.Second

const (
	// DefaultMaxResults bounds a search that doesn't ask for a size.
	DefaultMaxResults = 10
	// MaxResultsLimit caps what a caller can ask for.
	MaxResultsLimit = 50
)

// DefaultResultsCacheBytes bounds the total size of the results cache across
// all callers. Unlike the fetch cache it is not operator-configurable: search
// results are small (a page of titles, URLs, and snippets), so a fixed budget
// is plenty, and keeping it out of the flag surface avoids a knob no one
// tunes.
const DefaultResultsCacheBytes = 1 << 20 // 1 MiB

var ErrEmptyQuery = errors.New("query must not be empty")

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content,omitempty"`
}

// Searcher queries a SearXNG instance's JSON API.
type Searcher struct {
	baseURL string
	client  *http.Client
	results cache.Cache[[]Result] // completed searches, keyed by caller + query + limit
}

// New returns a Searcher querying the SearXNG instance at baseURL. A
// non-positive timeout falls back to DefaultTimeout. ttl is how long a
// completed search stays trusted before the query runs again; a non-positive
// value falls back to cache.DefaultTTL.
func New(baseURL string, timeout, ttl time.Duration) *Searcher {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if ttl <= 0 {
		ttl = cache.DefaultTTL
	}
	return &Searcher{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
		results: cache.New[[]Result](DefaultResultsCacheBytes, ttl),
	}
}

// Search runs query against SearXNG and returns at most limit results
// (DefaultMaxResults when limit is non-positive, MaxResultsLimit at most).
//
// cacheKey scopes the results cache to the caller (the authenticated agent in
// the MCP layer, so one agent's cached results can never be served to
// another); an empty key disables caching for this call.
func (s *Searcher) Search(ctx context.Context, query string, limit int, cacheKey string) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, ErrEmptyQuery
	}
	if limit <= 0 {
		limit = DefaultMaxResults
	}
	if limit > MaxResultsLimit {
		limit = MaxResultsLimit
	}

	// The key uses the normalized limit, so Search(q, 0) and Search(q, 10)
	// share one entry: they ask for exactly the same thing.
	var key string
	if cacheKey != "" {
		key = cacheKey + "\x00" + query + "\x00" + strconv.Itoa(limit)
	}
	if key != "" {
		if results, ok := s.results.Get(key); ok {
			return results, nil
		}
	}

	endpoint, err := url.Parse(s.baseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("parse searxng url: %w", err)
	}
	endpoint.RawQuery = url.Values{
		"q":      {query},
		"format": {"json"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A SearXNG instance with the JSON format disabled answers 403 here,
		// which is the single most common misconfiguration — name it rather
		// than reporting a bare status and leaving the operator guessing.
		if resp.StatusCode == http.StatusForbidden {
			return nil, errors.New("searxng returned 403: the instance likely has the json format disabled (add json to search.formats in settings.yml)")
		}
		return nil, fmt.Errorf("searxng returned unexpected status %s", resp.Status)
	}

	var payload struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode searxng response: %w", err)
	}

	if len(payload.Results) > limit {
		payload.Results = payload.Results[:limit]
	}
	if payload.Results == nil {
		payload.Results = []Result{}
	}

	// Only successes are cached: a failure must stay retryable, and storing
	// an error would pin it for the whole TTL. The size is the sum of the
	// fields an LLM actually sees, which is what the budget is protecting.
	if key != "" {
		size := int64(0)
		for i := range payload.Results {
			size += int64(len(payload.Results[i].Title) + len(payload.Results[i].URL) + len(payload.Results[i].Content))
		}
		s.results.Put(key, payload.Results, size)
	}
	return payload.Results, nil
}