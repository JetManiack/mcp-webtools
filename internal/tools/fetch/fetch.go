// Package fetch retrieves the content of a URL on behalf of an agent.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JetManiack/mcp-webtools/internal/cache"
)

// DefaultTimeout and DefaultMaxBytes are used when New is given
// non-positive values.
const (
	DefaultTimeout = 15 * time.Second
	// DefaultMaxBytes is the default page size. It is deliberately an
	// LLM-context-sized amount of text (~15-20k tokens) rather than a large
	// cap: a 2 MiB page would not fit in any context window, which would make
	// the pagination below useless for its main job. Operators with generous
	// context windows can raise it with --fetch-max-bytes.
	DefaultMaxBytes = 64 << 10 // 64 KiB

	// DefaultCacheMaxBytes bounds the total size of the document snapshot
	// cache across all callers. It is a memory budget, not a per-document
	// cap: entries are evicted least-recently-used until the budget holds.
	DefaultCacheMaxBytes = 32 << 20 // 32 MiB

	// defaultCaptureBudget is how long a first fetch spends trying to
	// capture the rest of the document for the snapshot cache after the
	// first page has been read. A document that doesn't finish in that time
	// (huge or slow) simply isn't cached and paginates by re-reading the
	// stream — the cost of a cache miss is a few extra origin reads, while
	// the cost of waiting it out could be the whole request timeout.
	defaultCaptureBudget = 3 * time.Second
)

var (
	ErrEmptyURL       = errors.New("url must not be empty")
	ErrNegativeOffset = errors.New("offset must not be negative")
)

// Result is one page of a fetched document.
//
// A document longer than the Fetcher's maxBytes comes back in pages: Content
// holds up to maxBytes of the body starting at Offset, Truncated says whether
// the document continues, and NextOffset is where the next page starts.
// NextOffset is 0 when the whole remaining document was returned, so an
// agent can loop "call with the last next_offset, stop when truncated is
// false" without tracking state.
type Result struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type,omitempty"`
	Offset      int64  `json:"offset"`
	Content     string `json:"content"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
	NextOffset  int64  `json:"next_offset"`
}

// Fetcher performs HTTP GETs with a bounded timeout and a page size, and
// keeps two bounded caches so the origin is contacted as little as possible:
// a snapshot cache of whole documents (paginating one re-downloads nothing)
// and a result cache of completed pages (repeating one re-sends nothing).
type Fetcher struct {
	client      *http.Client
	maxBytes    int64
	cacheBudget int64                 // byte budget of both caches, set by New
	snapshots   cache.Cache[docEntry] // whole documents, keyed by caller + URL
	results     cache.Cache[Result]   // completed pages, keyed by caller + URL + offset
	capture     time.Duration         // how long a first fetch spends capturing a snapshot
}

// New returns a Fetcher. A non-positive timeout or maxBytes falls back to
// the package defaults — an unbounded HTTP client is never an acceptable
// configuration here, since the URL comes from an agent and a slow or
// enormous response would otherwise pin a request goroutine and its memory
// indefinitely. cacheBudget is the byte budget of both caches; a non-positive
// value falls back to DefaultCacheMaxBytes. It is raised to at least maxBytes,
// because a page must always fit in the snapshot cache. ttl is how long
// snapshots and results stay trusted; a non-positive value falls back to
// cache.DefaultTTL.
func New(timeout time.Duration, maxBytes, cacheBudget int64, ttl time.Duration) *Fetcher {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if cacheBudget <= 0 {
		cacheBudget = DefaultCacheMaxBytes
	}
	if cacheBudget < maxBytes {
		cacheBudget = maxBytes
	}
	if ttl <= 0 {
		ttl = cache.DefaultTTL
	}
	return &Fetcher{
		client:      &http.Client{Timeout: timeout},
		maxBytes:    maxBytes,
		cacheBudget: cacheBudget,
		snapshots:   cache.New[docEntry](cacheBudget, ttl),
		results:     cache.New[Result](cacheBudget, ttl),
		capture:     defaultCaptureBudget,
	}
}

// Fetch GETs rawURL and returns the page of its body starting at byte
// offset, up to the Fetcher's maxBytes. A non-2xx status is an error: an
// agent asking for a page's content is not served by handing it an error
// page's body as if it were the answer.
//
// cacheKey scopes the snapshot cache to the caller (the authenticated agent
// in the MCP layer, so one agent's cached document can never be served to
// another); an empty key disables caching for this call.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, offset int64, cacheKey string) (*Result, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, ErrEmptyURL
	}
	if offset < 0 {
		return nil, ErrNegativeOffset
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

	snapKey, resultKey := "", ""
	if cacheKey != "" {
		snapKey = cacheKey + "\x00" + rawURL
		resultKey = snapKey + "\x00" + strconv.FormatInt(offset, 10)
	}

	// An exact repeat of a request whose result is still cached: no origin
	// contact of any kind.
	if resultKey != "" {
		if result, ok := f.results.Get(resultKey); ok {
			out := result
			return &out, nil
		}
	}

	// A snapshot exists for this document: the page is served from memory and
	// the origin is not contacted. This is the whole point of the snapshot
	// cache — paginating a large document must not re-download it per page.
	if snapKey != "" {
		if entry, ok := f.snapshots.Get(snapKey); ok {
			return f.pageResult(parsed, entry.statusCode, entry.contentType, entry.body, offset), nil
		}
	}

	resp, err := f.doGet(ctx, parsed)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", parsed.Redacted(), resp.Status)
	}
	contentType := resp.Header.Get("Content-Type")

	// Only a first fetch captures a snapshot, and only when the document is
	// not known to exceed the cache budget (a Content-Length over the budget
	// means the capture would read up to the whole budget to store nothing).
	// Everything else — continuations with no cached snapshot, oversized
	// documents, uncached callers — reads its page straight from the stream,
	// exactly as a fetch would have before the cache existed.
	if snapKey != "" && offset == 0 && (resp.ContentLength <= 0 || resp.ContentLength <= f.cacheBudget) {
		result, captured, err := f.captureAndPage(ctx, parsed, resp, contentType, snapKey)
		if err == nil && !captured {
			f.storeResult(resultKey, result)
		}
		return result, err
	}
	result, err := f.pageFromStream(parsed, resp.StatusCode, contentType, resp.Body, offset)
	if err == nil {
		f.storeResult(resultKey, result)
	}
	return result, err
}

// storeResult keeps a completed page in the result cache so an identical
// repeat (same caller, URL, offset) within the TTL is served from memory.
// Callers skip it for pages a snapshot will keep serving and for uncached
// calls; failed fetches are never stored, so an origin failure stays
// retryable.
func (f *Fetcher) storeResult(key string, result *Result) {
	if key == "" || result == nil {
		return
	}
	f.results.Put(key, *result, int64(len(result.Content)))
}

// doGet performs the GET. The body is returned still open; the caller owns
// closing it.
func (f *Fetcher) doGet(ctx context.Context, rawURL *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	return resp, nil
}

// captureAndPage reads the first page from the stream (so the first page
// returns as fast as the stream delivers it), then tries to capture the rest
// of the document into the snapshot cache within the capture budget. When
// the capture completes, the page is served from the snapshot; when it
// doesn't (a slow or oversized body), the already-read page is served anyway
// and later pages fall back to re-reading the stream. Caching must never
// make a fetch that used to work fail. It reports whether the document ended
// up in the snapshot cache: when it did, repeats are served from the snapshot
// and the result cache is left alone; when it didn't, the caller stores the
// page in the result cache instead.
func (f *Fetcher) captureAndPage(ctx context.Context, urlRef *url.URL, resp *http.Response, contentType, key string) (*Result, bool, error) {
	page, _, err := readChunk(resp.Body, f.maxBytes, 0)
	if err != nil {
		return nil, false, fmt.Errorf("read body: %w", err)
	}

	rest, done := f.captureRest(ctx, resp.Body, f.cacheBudget-int64(len(page)))
	if !done {
		resp.Body.Close() // stop the capture reader; the page is already in hand
		return f.pageResult(urlRef, resp.StatusCode, contentType, page, 0), false, nil
	}

	body := append(append([]byte(nil), page...), rest...)
	captured := false
	if int64(len(body)) <= f.cacheBudget {
		f.snapshots.Put(key, docEntry{
			body:        body,
			statusCode:  resp.StatusCode,
			contentType: contentType,
		}, int64(len(body)))
		captured = true
	}
	return f.pageResult(urlRef, resp.StatusCode, contentType, body, 0), captured, nil
}

// captureRest reads up to limit bytes of the rest of body in a goroutine,
// waiting at most f.capture for it to finish. It reports whether the body
// ended within what was read — a snapshot must be the whole document, not a
// prefix, or a later page would silently serve shifted bytes.
func (f *Fetcher) captureRest(ctx context.Context, body io.Reader, limit int64) ([]byte, bool) {
	type capture struct {
		rest []byte
		over bool // the body had more data than the read reached
	}
	result := make(chan capture, 1)
	go func() {
		rest, _ := io.ReadAll(io.LimitReader(body, limit))
		over := !bodyEnded(body)
		result <- capture{rest: rest, over: over}
	}()

	timer := time.NewTimer(f.capture)
	defer timer.Stop()
	select {
	case cap := <-result:
		return cap.rest, !cap.over
	case <-timer.C:
		return nil, false
	}
}

// bodyEnded reports whether body's stream ended exactly at the current read
// position, by probing one or two bytes past it. A byte means more data
// follows; a clean EOF means the body ended (or the stream died, which also
// means the read so far is not a trustworthy whole).
func bodyEnded(body io.Reader) bool {
	var probe [2]byte
	n, err := io.ReadFull(body, probe[:])
	switch {
	case err == nil:
		return false // two or more bytes remain
	case n == 0:
		return errors.Is(err, io.EOF)
	}
	// n == 1: one byte remained. Some transports deliver the final byte with
	// an early EOF, so probe once more to tell "last byte" from "real error".
	var next [1]byte
	_, err = io.ReadFull(body, next[:])
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF)
}

// pageFromStream serves a page from a live response stream that cannot (or
// need not) be cached: skip to offset and read one page.
func (f *Fetcher) pageFromStream(urlRef *url.URL, statusCode int, contentType string, body io.Reader, offset int64) (*Result, error) {
	page, truncated, err := readChunk(body, f.maxBytes, offset)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(page)) > f.maxBytes {
		page = page[:f.maxBytes] // drop the +1 probe byte; the document continues past it
	}
	content := cutToUTF8Boundary(page)
	nextOffset := int64(0)
	if truncated {
		nextOffset = offset + int64(len(content))
	}
	return &Result{
		URL:         urlRef.Redacted(),
		StatusCode:  statusCode,
		ContentType: contentType,
		Offset:      offset,
		Content:     string(content),
		Bytes:       int64(len(content)),
		Truncated:   truncated,
		NextOffset:  nextOffset,
	}, nil
}

// pageResult builds a Result for the page of body starting at offset.
func (f *Fetcher) pageResult(urlRef *url.URL, statusCode int, contentType string, body []byte, offset int64) *Result {
	content, truncated, nextOffset := pageFrom(body, offset, f.maxBytes)
	return &Result{
		URL:         urlRef.Redacted(),
		StatusCode:  statusCode,
		ContentType: contentType,
		Offset:      offset,
		Content:     content,
		Bytes:       int64(len(content)),
		Truncated:   truncated,
		NextOffset:  nextOffset,
	}
}

// pageFrom bounds body[offset:] to one page of at most maxBytes and reports
// whether the body continues past it.
func pageFrom(body []byte, offset, maxBytes int64) (content string, truncated bool, nextOffset int64) {
	if int64(len(body)) <= offset {
		// The document ended before the requested offset: no content left.
		return "", false, 0
	}
	end := offset + maxBytes
	if end > int64(len(body)) {
		end = int64(len(body))
	}
	chunk := body[offset:end]
	if int64(len(body)) > offset+maxBytes {
		truncated = true
		chunk = cutToUTF8Boundary(chunk)
		nextOffset = offset + int64(len(chunk))
	}
	return string(chunk), truncated, nextOffset
}

// readChunk reads up to maxBytes+1 of body starting at byte offset and
// reports whether the body continues past the returned bytes.
//
// It re-reads the stream and discards the first offset bytes rather than
// using a Range request: Range support is not universal (a server answering
// 200 to a Range request would need a whole different code path to detect),
// and re-reading is always correct. One byte past the limit is read so a
// body landing exactly on maxBytes isn't reported as truncated; that probe
// byte is part of the returned bytes.
func readChunk(body io.Reader, maxBytes, offset int64) ([]byte, bool, error) {
	if offset > 0 {
		n, err := io.CopyN(io.Discard, body, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, fmt.Errorf("skip to offset %d: %w", offset, err)
		}
		if n < offset {
			// The document ended before the requested offset: no content left.
			return []byte{}, false, nil
		}
	}

	buf, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	return buf, int64(len(buf)) > maxBytes, nil
}

// cutToUTF8Boundary drops any trailing bytes of an incomplete or invalid
// UTF-8 rune from b.
//
// Cutting a multi-byte rune at a byte limit would leave the page ending in a
// broken character, and — since the next page resumes at len(b) — the next
// page would start mid-rune and be invalid UTF-8 too. Cutting back to the
// rune boundary keeps both pages valid: the character reappears, whole, at
// the start of the continuation.
func cutToUTF8Boundary(b []byte) []byte {
	for len(b) > 0 {
		r, size := utf8.DecodeLastRune(b)
		if r == utf8.RuneError && size <= 1 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

// docEntry is one cached document snapshot: the body exactly as the origin
// sent it (untrimmed — a page cut mid-rune is only trimmed when rendered
// into a Result, never in the snapshot), plus the response metadata needed
// to rebuild a Result for any offset. TTL and LRU bookkeeping live in
// cache.entry.
type docEntry struct {
	body        []byte
	statusCode  int
	contentType string
}
