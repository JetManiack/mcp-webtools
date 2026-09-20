package search

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resultsJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"title":"result %d","url":"https://example.com/%d","content":"snippet %d"}`, i, i, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestSearchSuccess(t *testing.T) {
	var gotQuery, gotFormat string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		_, _ = w.Write([]byte(resultsJSON(2)))
	}))
	defer server.Close()

	results, err := New(server.URL, 0, 0).Search(context.Background(), "go mcp server", 0, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "result 0" || results[0].URL != "https://example.com/0" || results[0].Content != "snippet 0" {
		t.Errorf("first result = %+v, want the server's first hit", results[0])
	}
	// Query must survive as text, not as pre-encoded or truncated-at-space
	// junk — this is the bug a naive fmt.Sprintf URL introduces.
	if gotQuery != "go mcp server" {
		t.Errorf("q = %q, want %q", gotQuery, "go mcp server")
	}
	if gotFormat != "json" {
		t.Errorf("format = %q, want json", gotFormat)
	}
}

func TestSearchTrimsBaseURLSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search (a trailing slash on the base URL must not double up)", r.URL.Path)
		}
		_, _ = w.Write([]byte(resultsJSON(1)))
	}))
	defer server.Close()

	if _, err := New(server.URL+"/", 0, 0).Search(context.Background(), "q", 0, ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

// An empty cacheKey disables caching, which the subtests rely on: they reuse
// one Searcher and several ask for the same normalized limit, so a non-empty
// key would let one subtest serve another from the cache.
func TestSearchLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(resultsJSON(80)))
	}))
	defer server.Close()

	searcher := New(server.URL, 0, 0)
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "default", limit: 0, want: DefaultMaxResults},
		{name: "negative falls back to default", limit: -5, want: DefaultMaxResults},
		{name: "explicit", limit: 3, want: 3},
		{name: "above the cap", limit: 999, want: MaxResultsLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := searcher.Search(context.Background(), "q", tt.limit, "")
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(results) != tt.want {
				t.Errorf("got %d results, want %d", len(results), tt.want)
			}
		})
	}
}

func TestSearchEmptyResultsIsEmptySliceNotNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":null}`))
	}))
	defer server.Close()

	results, err := New(server.URL, 0, 0).Search(context.Background(), "q", 0, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results == nil {
		t.Error("results = nil, want an empty slice (nil serializes to JSON null, which clients have to special-case)")
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	for _, query := range []string{"", "   "} {
		if _, err := New("http://unused", 0, 0).Search(context.Background(), query, 0, ""); !errors.Is(err, ErrEmptyQuery) {
			t.Errorf("query %q: error = %v, want ErrEmptyQuery", query, err)
		}
	}
}

// 403 is what a SearXNG instance with the JSON format disabled returns, and is
// by far the most common way this tool fails in practice — the error has to
// name that cause instead of reporting a bare status.
func TestSearch403NamesTheJSONFormatCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, err := New(server.URL, 0, 0).Search(context.Background(), "q", 0, "")
	if err == nil {
		t.Fatal("Search succeeded, want error")
	}
	if !strings.Contains(err.Error(), "json format") {
		t.Errorf("error = %q, want it to mention the json format", err)
	}
}

func TestSearchOtherStatusIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	if _, err := New(server.URL, 0, 0).Search(context.Background(), "q", 0, ""); err == nil {
		t.Fatal("Search succeeded, want error")
	}
}

func TestSearchMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results": [`))
	}))
	defer server.Close()

	_, err := New(server.URL, 0, 0).Search(context.Background(), "q", 0, "")
	if err == nil {
		t.Fatal("Search succeeded, want decode error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %q, want it to mention decoding", err)
	}
}

func TestSearchHonorsConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	start := time.Now()
	if _, err := New(server.URL, 50*time.Millisecond, 0).Search(context.Background(), "q", 0, ""); err == nil {
		t.Fatal("Search succeeded, want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v — the configured timeout was not applied", elapsed)
	}
}

func TestNewFallsBackToDefaultTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		if got := New("http://unused", timeout, 0).client.Timeout; got != DefaultTimeout {
			t.Errorf("timeout %v: client.Timeout = %v, want %v", timeout, got, DefaultTimeout)
		}
	}
}

// A repeated, fully-identical search is served from the results cache, not
// the instance: the origin (the SearXNG proxy here) must be hit exactly once.
func TestSearchRepeatedQueryHitsOriginOnce(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(resultsJSON(3)))
	}))
	defer server.Close()

	searcher := New(server.URL, 0, 0)
	for i := 0; i < 3; i++ {
		results, err := searcher.Search(context.Background(), "go mcp server", 0, "agent-1")
		if err != nil {
			t.Fatalf("Search %d: %v", i, err)
		}
		if len(results) != 3 {
			t.Fatalf("Search %d: got %d results, want 3", i, len(results))
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("origin was hit %d times for 3 identical searches, want 1", got)
	}
}

// The cache key includes the normalized limit, so a different page size is a
// different request and must reach the instance again.
func TestSearchDifferentLimitIsNotCached(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(resultsJSON(50)))
	}))
	defer server.Close()

	searcher := New(server.URL, 0, 0)
	if _, err := searcher.Search(context.Background(), "q", 5, "agent-1"); err != nil {
		t.Fatalf("Search limit 5: %v", err)
	}
	if _, err := searcher.Search(context.Background(), "q", 5, "agent-1"); err != nil {
		t.Fatalf("repeat Search limit 5: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin was hit %d times before the different limit, want 1", got)
	}
	if _, err := searcher.Search(context.Background(), "q", 10, "agent-1"); err != nil {
		t.Fatalf("Search limit 10: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times after a different limit, want 2", got)
	}
}

// The cache key includes the caller, so one agent's cached search can never
// be served to another: each key must be its own entry and its own origin hit.
func TestSearchCacheIsScopedPerCaller(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(resultsJSON(2)))
	}))
	defer server.Close()

	searcher := New(server.URL, 0, 0)
	for _, agent := range []string{"agent-1", "agent-2"} {
		for i := 0; i < 2; i++ {
			if _, err := searcher.Search(context.Background(), "q", 0, agent); err != nil {
				t.Fatalf("Search as %s (%d): %v", agent, i, err)
			}
		}
	}
	// Two agents, two searches each: two origin hits, not four and not one.
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times for two agents, want 2 (one per agent)", got)
	}
}

// An expired cache entry must be re-queried: a search result is a snapshot
// of a moving index, not a fact.
func TestSearchExpiredResultIsRequeried(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(resultsJSON(2)))
	}))
	defer server.Close()

	searcher := New(server.URL, 0, 0)
	if _, err := searcher.Search(context.Background(), "q", 0, "agent-1"); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	searcher.results.ExpireAllForTesting()
	if _, err := searcher.Search(context.Background(), "q", 0, "agent-1"); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times after expiry, want 2", got)
	}
}
