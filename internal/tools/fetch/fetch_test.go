package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFetchSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello world"))
	}))
	defer server.Close()

	result, err := New(0, 0, 0, 0).Fetch(context.Background(), server.URL, 0, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Content != "hello world" {
		t.Errorf("Content = %q, want %q", result.Content, "hello world")
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", result.StatusCode)
	}
	if result.Bytes != 11 {
		t.Errorf("Bytes = %d, want 11", result.Bytes)
	}
	if result.Truncated {
		t.Error("Truncated = true, want false")
	}
	if result.Offset != 0 {
		t.Errorf("Offset = %d, want 0", result.Offset)
	}
	if result.NextOffset != 0 {
		t.Errorf("NextOffset = %d, want 0 (no continuation)", result.NextOffset)
	}
	if !strings.HasPrefix(result.ContentType, "text/plain") {
		t.Errorf("ContentType = %q, want text/plain…", result.ContentType)
	}
}

func TestFetchNon2xxIsError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusMovedPermanently} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("an error page, not the content the agent asked for"))
		}))

		// A redirect is followed by the client, so 301 only surfaces here when
		// there's no Location header — which is exactly the "broken response"
		// case that must not be handed back as content.
		_, err := New(0, 0, 0, 0).Fetch(context.Background(), server.URL, 0, "")
		if err == nil {
			t.Errorf("status %d: Fetch succeeded, want error", status)
		}
		server.Close()
	}
}

func TestFetchTruncatesAtMaxBytes(t *testing.T) {
	body := strings.Repeat("x", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	result, err := New(0, 10, 0, 0).Fetch(context.Background(), server.URL, 0, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(result.Content) != 10 {
		t.Errorf("len(Content) = %d, want 10", len(result.Content))
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
	if result.Bytes != 10 {
		t.Errorf("Bytes = %d, want 10", result.Bytes)
	}
	if result.NextOffset != 10 {
		t.Errorf("NextOffset = %d, want 10", result.NextOffset)
	}
}

// A body landing exactly on the limit is complete, not truncated — the
// off-by-one here is the difference between an honest flag and one that cries
// wolf on every response that happens to fit.
func TestFetchExactlyMaxBytesIsNotTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("y", 10)))
	}))
	defer server.Close()

	result, err := New(0, 10, 0, 0).Fetch(context.Background(), server.URL, 0, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Truncated {
		t.Error("Truncated = true, want false")
	}
	if result.NextOffset != 0 {
		t.Errorf("NextOffset = %d, want 0 (no continuation)", result.NextOffset)
	}
	if result.Content != strings.Repeat("y", 10) {
		t.Errorf("Content = %q, want 10 y's", result.Content)
	}
}

// Walking a document to the end page by page must return it whole, in order,
// with no lost or duplicated bytes.
func TestFetchPaginatesWholeDocument(t *testing.T) {
	body := strings.Repeat("x", 250)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 100, 0, 0)
	var reassembled strings.Builder
	offset := int64(0)
	for i := 0; i < 5; i++ {
		result, err := fetcher.Fetch(context.Background(), server.URL, offset, "")
		if err != nil {
			t.Fatalf("page %d: Fetch: %v", i, err)
		}
		if result.Offset != offset {
			t.Errorf("page %d: Offset = %d, want %d", i, result.Offset, offset)
		}
		reassembled.WriteString(result.Content)
		if result.Bytes != int64(len(result.Content)) {
			t.Errorf("page %d: Bytes = %d, want %d", i, result.Bytes, len(result.Content))
		}
		if !result.Truncated {
			break
		}
		if result.NextOffset <= offset {
			t.Fatalf("page %d: NextOffset = %d, must advance past Offset %d", i, result.NextOffset, offset)
		}
		offset = result.NextOffset
	}
	if got := reassembled.String(); got != body {
		t.Fatalf("pages reassemble to %d bytes, want the full %d-byte document", len(got), len(body))
	}
}

// A truncation cut can land mid-rune; the page must then end on a rune
// boundary so that both this page and the continuation (which resumes at the
// cut) are valid UTF-8.
func TestFetchTruncationEndsOnRuneBoundary(t *testing.T) {
	// "Ж" is two UTF-8 bytes; 60 runes is 120 bytes, so a 119-byte limit cuts
	// the 60th rune in half.
	body := strings.Repeat("Ж", 60)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 119, 0, 0)
	first, err := fetcher.Fetch(context.Background(), server.URL, 0, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !first.Truncated {
		t.Fatal("first page: Truncated = false, want true")
	}
	if !utf8.ValidString(first.Content) {
		t.Error("first page is not valid UTF-8 — the cut split a rune")
	}
	if !strings.HasSuffix(first.Content, "Ж") {
		t.Errorf("first page should end on a complete rune, got tail %q", first.Content[len(first.Content)-4:])
	}
	if first.NextOffset != int64(len(first.Content)) {
		t.Errorf("NextOffset = %d, want %d (len of the trimmed page)", first.NextOffset, len(first.Content))
	}

	second, err := fetcher.Fetch(context.Background(), server.URL, first.NextOffset, "")
	if err != nil {
		t.Fatalf("continuation Fetch: %v", err)
	}
	if !utf8.ValidString(second.Content) {
		t.Error("continuation is not valid UTF-8 — it resumed mid-rune")
	}
	if second.Truncated || second.NextOffset != 0 {
		t.Errorf("continuation = truncated %v, next %d; want the final page", second.Truncated, second.NextOffset)
	}
	if got := first.Content + second.Content; got != body {
		t.Errorf("pages reassemble to %d bytes of %d, want the full document", len(got), len(body))
	}
}

// Asking past the end of a document is not an error — the agent may have
// guessed the size wrong; the honest answer is an empty, untruncated page.
func TestFetchOffsetBeyondEndIsEmptyPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ten bytes "))
	}))
	defer server.Close()

	result, err := New(0, 100, 0, 0).Fetch(context.Background(), server.URL, 50, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Content != "" {
		t.Errorf("Content = %q, want empty", result.Content)
	}
	if result.Truncated || result.NextOffset != 0 || result.Bytes != 0 {
		t.Errorf("page past the end = truncated %v, next %d, bytes %d; want all zero/false",
			result.Truncated, result.NextOffset, result.Bytes)
	}
}

func TestFetchRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		offset  int64
		wantErr error
	}{
		{name: "empty", url: "", wantErr: ErrEmptyURL},
		{name: "whitespace only", url: "   ", wantErr: ErrEmptyURL},
		{name: "file scheme", url: "file:///etc/passwd"},
		{name: "no scheme", url: "example.com/path"},
		{name: "negative offset", url: "http://example.com", offset: -1, wantErr: ErrNegativeOffset},
	}

	fetcher := New(0, 0, 0, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fetcher.Fetch(context.Background(), tt.url, tt.offset, "")
			if err == nil {
				t.Fatal("Fetch succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestFetchHonorsConfiguredTimeout(t *testing.T) {
	// The handler waits for the client to give up rather than sleeping a fixed
	// duration, so the test costs exactly as long as the timeout under test.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	start := time.Now()
	_, err := New(50*time.Millisecond, 0, 0, 0).Fetch(context.Background(), server.URL, 0, "")
	if err == nil {
		t.Fatal("Fetch succeeded, want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v — the configured timeout was not applied", elapsed)
	}
}

func TestFetchHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if _, err := New(0, 0, 0, 0).Fetch(ctx, server.URL, 0, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// The origin is contacted exactly once per document, no matter how many
// pages the agent walks through it.
func TestFetchContinuationIsServedFromSnapshot(t *testing.T) {
	body := strings.Repeat("x", 250)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 100, 0, 0)
	var reassembled strings.Builder
	offset := int64(0)
	for i := 0; i < 5; i++ {
		result, err := fetcher.Fetch(context.Background(), server.URL, offset, "agent-1")
		if err != nil {
			t.Fatalf("page %d: Fetch: %v", i, err)
		}
		reassembled.WriteString(result.Content)
		if !result.Truncated {
			break
		}
		offset = result.NextOffset
	}
	if got := reassembled.String(); got != body {
		t.Errorf("pages reassemble to %d bytes, want the full %d-byte document", len(got), len(body))
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("origin was hit %d times, want exactly 1 (continuations must be served from the snapshot)", got)
	}
}

// The snapshot cache is scoped by cacheKey: two callers (two agents) for the
// same URL must each hit the origin, never share a snapshot.
func TestFetchCacheIsScopedByCacheKey(t *testing.T) {
	body := strings.Repeat("x", 250)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 100, 0, 0)
	if _, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch (agent-1): %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-2"); err != nil {
		t.Fatalf("Fetch (agent-2): %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times, want 2 (one per caller)", got)
	}
}

// A snapshot past its TTL is not trusted: the origin may have changed, so
// the document is fetched again.
func TestFetchExpiredSnapshotIsRefetched(t *testing.T) {
	body := strings.Repeat("x", 250)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 100, 0, 0)
	if _, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	fetcher.snapshots.ExpireAllForTesting()
	if _, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch after expiry: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times, want 2 (expired snapshot must not be served)", got)
	}
}

// The snapshot cache budget is total: a new entry evicts least-recently-used
// ones until it fits, so re-fetching an evicted document re-contacts the
// origin.
func TestFetchCacheEvictsWhenOverBudget(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		_, _ = w.Write([]byte(strings.Repeat("a", 120)))
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		_, _ = w.Write([]byte(strings.Repeat("b", 100)))
	}))
	defer serverB.Close()

	fetcher := New(0, 50, 200, 0) // budget 200: both documents fit alone, not together
	if _, err := fetcher.Fetch(context.Background(), serverA.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch A: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), serverB.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch B: %v", err)
	}
	if used := fetcher.snapshots.Used(); used > 200 {
		t.Errorf("snapshot cache used %d bytes, over the 200 budget", used)
	}
	if _, err := fetcher.Fetch(context.Background(), serverA.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch A again: %v", err)
	}
	if got := hitsA.Load(); got != 2 {
		t.Errorf("document A was fetched %d times, want 2 (it was evicted by B)", got)
	}
	if got := hitsB.Load(); got != 1 {
		t.Errorf("document B was fetched %d times, want 1", got)
	}
}

// A document known (by Content-Length) to exceed the cache budget is never
// captured into the snapshot cache, but each completed page lands in the
// result cache: repeating the exact same page re-contacts the origin no more
// than once.
func TestFetchOversizedDocumentPagesAreResultCached(t *testing.T) {
	body := strings.Repeat("x", 200)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 50, 100, 0) // 200-byte body does not fit the 100 snapshot budget
	if fetcher.snapshots.Len() != 0 {
		t.Fatal("test invariant broken")
	}
	first, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The same page again: served from the result cache, not the origin.
	repeat, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1")
	if err != nil {
		t.Fatalf("repeat Fetch: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("origin was hit %d times after a repeated page, want 1 (result cache)", got)
	}
	if repeat.Content != first.Content || repeat.Truncated != first.Truncated || repeat.NextOffset != first.NextOffset {
		t.Errorf("cached repeat = %+v, want it identical to the first result %+v", repeat, first)
	}
	// A different page of the same document is a different request: it reads
	// the stream (and pages the rest of the document, 200 = 4 x 50).
	if _, err := fetcher.Fetch(context.Background(), server.URL, 50, "agent-1"); err != nil {
		t.Fatalf("page 2 Fetch: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times after a different page, want 2", got)
	}
	if fetcher.snapshots.Len() != 0 {
		t.Errorf("snapshot cache holds %d entries, want none for an oversized document", fetcher.snapshots.Len())
	}
}

// An expired result cache entry is refetched, like an expired snapshot.
func TestFetchExpiredResultIsRefetched(t *testing.T) {
	body := strings.Repeat("x", 200)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := New(0, 50, 100, 0) // oversized body: pages live in the result cache only
	if _, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	fetcher.results.ExpireAllForTesting()
	if _, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1"); err != nil {
		t.Fatalf("Fetch after expiry: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("origin was hit %d times, want 2 (expired result must not be served)", got)
	}
}

// A chunked (no Content-Length) body that fits the budget is captured too,
// and its continuation is served from the snapshot.
func TestFetchChunkedBodyIsCachedWhenItFits(t *testing.T) {
	body := strings.Repeat("x", 90)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(body[:30]))
		flusher.Flush() // a mid-write flush forces chunked encoding, no Content-Length
		_, _ = w.Write([]byte(body[30:]))
		flusher.Flush()
	}))
	defer server.Close()

	fetcher := New(0, 50, 100, 0)
	first, err := fetcher.Fetch(context.Background(), server.URL, 0, "agent-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !first.Truncated || first.NextOffset != 50 {
		t.Fatalf("first page = truncated %v, next %d; want truncated at 50", first.Truncated, first.NextOffset)
	}
	second, err := fetcher.Fetch(context.Background(), server.URL, 50, "agent-1")
	if err != nil {
		t.Fatalf("continuation Fetch: %v", err)
	}
	if got := first.Content + second.Content; got != body {
		t.Errorf("pages reassemble to %d bytes of %q, want the full document", len(got), got)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("origin was hit %d times, want 1 (chunked body fit the budget and was captured)", got)
	}
}

// New must never produce an unbounded client: a zero or negative timeout is a
// misconfiguration, and silently accepting it would let one slow URL pin a
// request goroutine forever. A cache budget below the page size is raised to
// the page size, since a page must always fit in the snapshot cache.
func TestNewFallsBackToDefaults(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		f := New(timeout, -1, -1, 0)
		if f.client.Timeout != DefaultTimeout {
			t.Errorf("timeout %v: client.Timeout = %v, want %v", timeout, f.client.Timeout, DefaultTimeout)
		}
		if f.maxBytes != DefaultMaxBytes {
			t.Errorf("timeout %v: maxBytes = %d, want %d", timeout, f.maxBytes, DefaultMaxBytes)
		}
		if f.snapshots.MaxBytes() != DefaultCacheMaxBytes {
			t.Errorf("timeout %v: cache budget = %d, want %d", timeout, f.snapshots.MaxBytes(), DefaultCacheMaxBytes)
		}
	}
	if f := New(0, 1<<20, 100, 0); f.snapshots.MaxBytes() != 1<<20 {
		t.Errorf("cache budget = %d, want it raised to the page size %d", f.snapshots.MaxBytes(), 1<<20)
	}
}
