package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/storage"
	"github.com/JetManiack/mcp-webtools/internal/tools/fetch"
	"github.com/JetManiack/mcp-webtools/internal/tools/search"
)

// bearerTransport attaches a fixed bearer token to every request, the way a
// configured MCP client would.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	if b.token != "" {
		clone.Header.Set("Authorization", "Bearer "+b.token)
	}
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// connectSession boots the real /mcp handler over HTTP and returns a connected
// MCP client session — the same path a real agent takes, so auth, transport,
// tool dispatch and history recording are all exercised together.
func connectSession(t *testing.T, deps Deps, token string) *mcp.ClientSession {
	t.Helper()

	server := httptest.NewServer(NewHTTPHandler(deps))
	t.Cleanup(server.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             server.URL,
		HTTPClient:           &http.Client{Transport: bearerTransport{token: token}},
		DisableStandaloneSSE: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func testDeps(db *gorm.DB, searxngURL string) Deps {
	return Deps{
		// A zero TTL keeps entries live for the whole test, so caching
		// behavior in these tests is deterministic.
		DB:       db,
		Fetcher:  fetch.New(2*time.Second, 1<<20, 0, 0),
		Searcher: search.New(searxngURL, 2*time.Second, 0),
		Version:  "test",
	}
}

func TestListToolsAdvertisesSchemas(t *testing.T) {
	db := openTestDB(t)
	_, token := mustAgentWithToken(t, db, "scraper-1")
	session := connectSession(t, testDeps(db, "http://unused"), token)

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		// A tool with no input schema is one an agent has to guess at — this is
		// what the old stringly-typed dispatcher failed to provide.
		if tool.InputSchema == nil {
			t.Errorf("tool %q advertises no input schema", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q advertises no description", tool.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"fetch", "search"}) {
		t.Errorf("tools = %v, want [fetch search]", names)
	}
}

func TestFetchToolEndToEndIsRecorded(t *testing.T) {
	db := openTestDB(t)
	agent, token := mustAgentWithToken(t, db, "scraper-1")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("the page body"))
	}))
	defer origin.Close()

	session := connectSession(t, testDeps(db, "http://unused"), token)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fetch",
		Arguments: map[string]any{"url": origin.URL},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool reported an error: %+v", result.Content)
	}

	var out fetch.FetchOutput
	structuredInto(t, result, &out)
	if out.Content != "the page body" {
		t.Errorf("Content = %q, want %q", out.Content, "the page body")
	}
	if out.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", out.StatusCode)
	}

	call := waitForOneHistoryRow(t, db)
	if call.Tool != "fetch" {
		t.Errorf("Tool = %q, want fetch", call.Tool)
	}
	if call.ActorID != agent.ID {
		t.Errorf("ActorID = %q, want the authenticated agent %q", call.ActorID, agent.ID)
	}
	if call.IsError {
		t.Errorf("IsError = true, want false for a successful call")
	}
	// The arguments have to be legible after the fact — that's the whole point
	// of keeping them.
	if !strings.Contains(call.InputJSON, origin.URL) {
		t.Errorf("InputJSON = %q, want it to contain the requested URL", call.InputJSON)
	}
	if !strings.Contains(call.OutputJSON, "the page body") {
		t.Errorf("OutputJSON = %q, want it to contain the response", call.OutputJSON)
	}
}

// A document longer than the page size comes back over several tool calls;
// the agent drives the continuation with the next_offset from each result.
func TestFetchToolPaginatesLargeBodies(t *testing.T) {
	db := openTestDB(t)
	_, token := mustAgentWithToken(t, db, "scraper-1")

	body := strings.Repeat("a", 200)
	var originHits int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&originHits, 1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()

	deps := testDeps(db, "http://unused")
	deps.Fetcher = fetch.New(2*time.Second, 64, 0, 0)
	session := connectSession(t, deps, token)

	var got strings.Builder
	pages := 0
	for {
		args := map[string]any{"url": origin.URL}
		if got.Len() > 0 {
			args["offset"] = got.Len()
		}
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "fetch",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("CallTool page %d: %v", pages, err)
		}
		if result.IsError {
			t.Fatalf("page %d reported an error: %+v", pages, result.Content)
		}

		var out fetch.FetchOutput
		structuredInto(t, result, &out)
		got.WriteString(out.Content)
		pages++
		if !out.Truncated {
			if out.NextOffset != 0 {
				t.Errorf("final page: NextOffset = %d, want 0", out.NextOffset)
			}
			break
		}
		if out.NextOffset != int64(got.Len()) {
			t.Fatalf("page %d: NextOffset = %d, want %d (len of what was delivered)", pages, out.NextOffset, got.Len())
		}
		if pages > 10 {
			t.Fatal("pagination never terminated")
		}
	}

	if got.String() != body {
		t.Errorf("pages reassemble to %d bytes, want the full %d-byte document", got.Len(), len(body))
	}
	if pages != 4 { // 200 = 64 + 64 + 64 + 8
		t.Errorf("used %d pages, want 4", pages)
	}
	// The whole point of the snapshot cache: four pages, one origin request.
	if hits := atomic.LoadInt64(&originHits); hits != 1 {
		t.Errorf("origin was hit %d times, want 1 (continuation pages must be served from the snapshot)", hits)
	}
}

func TestSearchToolEndToEnd(t *testing.T) {
	db := openTestDB(t)
	_, token := mustAgentWithToken(t, db, "scraper-1")

	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"Go","url":"https://go.dev","content":"the language"}]}`))
	}))
	defer searxng.Close()

	session := connectSession(t, testDeps(db, searxng.URL), token)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "go mcp", "limit": 5},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool reported an error: %+v", result.Content)
	}

	var out search.SearchOutput
	structuredInto(t, result, &out)
	if len(out.Results) != 1 || out.Results[0].URL != "https://go.dev" {
		t.Errorf("Results = %+v, want the single hit from SearXNG", out.Results)
	}

	call := waitForOneHistoryRow(t, db)
	if call.Tool != "search" {
		t.Errorf("Tool = %q, want search", call.Tool)
	}
	if !strings.Contains(call.InputJSON, "go mcp") {
		t.Errorf("InputJSON = %q, want it to contain the query", call.InputJSON)
	}
}

// A repeated, identical search must reach the SearXNG instance exactly once:
// the results cache serves the second call from memory. This is the full path
// — bearer auth, actor scoping, MCP dispatch — because the cache key comes
// from the authenticated identity, and a session that re-uses a stale key
// would break exactly this invariant.
func TestSearchToolRepeatHitsOriginOnce(t *testing.T) {
	db := openTestDB(t)
	_, token := mustAgentWithToken(t, db, "scraper-1")

	var hits int64
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"results":[{"title":"Go","url":"https://go.dev","content":"the language"}]}`))
	}))
	defer searxng.Close()

	session := connectSession(t, testDeps(db, searxng.URL), token)

	for i := 0; i < 2; i++ {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "go mcp", "limit": 5},
		})
		if err != nil {
			t.Fatalf("CallTool %d: %v", i, err)
		}
		if result.IsError {
			t.Fatalf("search %d reported an error: %+v", i, result.Content)
		}
		var out search.SearchOutput
		structuredInto(t, result, &out)
		if len(out.Results) != 1 {
			t.Fatalf("search %d: got %d results, want 1", i, len(out.Results))
		}
	}

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("the SearXNG instance was hit %d times for two identical searches, want 1", got)
	}
}

// A tool error must reach the agent as a tool error and land in history as one
// — history that only contains successes hides exactly what you go looking for.
func TestToolErrorIsReturnedAndRecorded(t *testing.T) {
	db := openTestDB(t)
	_, token := mustAgentWithToken(t, db, "scraper-1")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer origin.Close()

	session := connectSession(t, testDeps(db, "http://unused"), token)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fetch",
		Arguments: map[string]any{"url": origin.URL},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Error("IsError = false, want true for a 404 origin")
	}

	call := waitForOneHistoryRow(t, db)
	if !call.IsError {
		t.Errorf("IsError = false, want true for a failed call")
	}
	if call.OutputJSON == "" {
		t.Error("OutputJSON is empty for an error call")
	}
}

func TestUnauthenticatedSessionIsRefused(t *testing.T) {
	db := openTestDB(t)

	server := httptest.NewServer(NewHTTPHandler(testDeps(db, "http://unused")))
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             server.URL,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, transport, nil)
	if err == nil {
		_ = session.Close()
		t.Fatal("connected without a bearer token, want rejection")
	}
}

func TestNewServerReportsVersion(t *testing.T) {
	db := openTestDB(t)
	_, token := mustAgentWithToken(t, db, "scraper-1")

	deps := testDeps(db, "http://unused")
	deps.Version = ""
	session := connectSession(t, deps, token)

	// An unstamped build must still report something rather than an empty
	// version string.
	if got := session.InitializeResult().ServerInfo.Version; got != fallbackVersion {
		t.Errorf("server version = %q, want %q", got, fallbackVersion)
	}
	if got := session.InitializeResult().ServerInfo.Name; got != ServerName {
		t.Errorf("server name = %q, want %q", got, ServerName)
	}
}

// structuredInto decodes a tool result's structured output into v.
func structuredInto(t *testing.T, result *mcp.CallToolResult, v any) {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatal("result has no structured content")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, v); err != nil {
		t.Fatalf("decode structured content %s: %v", encoded, err)
	}
}

// waitForOneHistoryRow polls until exactly one history row exists. Recording
// happens on the server goroutine handling the call, which may still be
// finishing when the client already holds the response.
func waitForOneHistoryRow(t *testing.T, db *gorm.DB) storage.ToolCall {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		calls, _, err := storage.ListToolCalls(db, storage.HistoryFilter{})
		if err != nil {
			t.Fatalf("ListToolCalls: %v", err)
		}
		if len(calls) == 1 {
			return calls[0]
		}
		if len(calls) > 1 {
			t.Fatalf("got %d history rows, want 1", len(calls))
		}
		if time.Now().After(deadline) {
			t.Fatal("no history row was written within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
