// Package search queries a SearXNG instance on behalf of an agent.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
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
}

// New returns a Searcher querying the SearXNG instance at baseURL. A
// non-positive timeout falls back to DefaultTimeout.
func New(baseURL string, timeout time.Duration) *Searcher {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Searcher{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// Search runs query against SearXNG and returns at most limit results
// (DefaultMaxResults when limit is non-positive, MaxResultsLimit at most).
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, ErrEmptyQuery
	}
	if limit <= 0 {
		limit = DefaultMaxResults
	}
	if limit > MaxResultsLimit {
		limit = MaxResultsLimit
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
	return payload.Results, nil
}
