package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello world"))
	}))
	defer server.Close()

	result, err := New(0, 0).Fetch(context.Background(), server.URL)
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
		_, err := New(0, 0).Fetch(context.Background(), server.URL)
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

	result, err := New(0, 10).Fetch(context.Background(), server.URL)
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
}

// A body landing exactly on the limit is complete, not truncated — the
// off-by-one here is the difference between an honest flag and one that cries
// wolf on every response that happens to fit.
func TestFetchExactlyMaxBytesIsNotTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("y", 10)))
	}))
	defer server.Close()

	result, err := New(0, 10).Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Truncated {
		t.Error("Truncated = true, want false")
	}
	if result.Content != strings.Repeat("y", 10) {
		t.Errorf("Content = %q, want 10 y's", result.Content)
	}
}

func TestFetchRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr error
	}{
		{name: "empty", url: "", wantErr: ErrEmptyURL},
		{name: "whitespace only", url: "   ", wantErr: ErrEmptyURL},
		{name: "file scheme", url: "file:///etc/passwd"},
		{name: "no scheme", url: "example.com/path"},
	}

	fetcher := New(0, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fetcher.Fetch(context.Background(), tt.url)
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
	_, err := New(50*time.Millisecond, 0).Fetch(context.Background(), server.URL)
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

	if _, err := New(0, 0).Fetch(ctx, server.URL); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// New must never produce an unbounded client: a zero or negative timeout is a
// misconfiguration, and silently accepting it would let one slow URL pin a
// request goroutine forever.
func TestNewFallsBackToDefaults(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		f := New(timeout, -1)
		if f.client.Timeout != DefaultTimeout {
			t.Errorf("timeout %v: client.Timeout = %v, want %v", timeout, f.client.Timeout, DefaultTimeout)
		}
		if f.maxBytes != DefaultMaxBytes {
			t.Errorf("timeout %v: maxBytes = %d, want %d", timeout, f.maxBytes, DefaultMaxBytes)
		}
	}
}
