package search

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	results, err := New(server.URL, 0).Search(context.Background(), "go mcp server", 0)
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

	if _, err := New(server.URL+"/", 0).Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearchLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(resultsJSON(80)))
	}))
	defer server.Close()

	searcher := New(server.URL, 0)
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
			results, err := searcher.Search(context.Background(), "q", tt.limit)
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

	results, err := New(server.URL, 0).Search(context.Background(), "q", 0)
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
		if _, err := New("http://unused", 0).Search(context.Background(), query, 0); !errors.Is(err, ErrEmptyQuery) {
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

	_, err := New(server.URL, 0).Search(context.Background(), "q", 0)
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

	if _, err := New(server.URL, 0).Search(context.Background(), "q", 0); err == nil {
		t.Fatal("Search succeeded, want error")
	}
}

func TestSearchMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results": [`))
	}))
	defer server.Close()

	_, err := New(server.URL, 0).Search(context.Background(), "q", 0)
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
	if _, err := New(server.URL, 50*time.Millisecond).Search(context.Background(), "q", 0); err == nil {
		t.Fatal("Search succeeded, want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v — the configured timeout was not applied", elapsed)
	}
}

func TestNewFallsBackToDefaultTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		if got := New("http://unused", timeout).client.Timeout; got != DefaultTimeout {
			t.Errorf("timeout %v: client.Timeout = %v, want %v", timeout, got, DefaultTimeout)
		}
	}
}
