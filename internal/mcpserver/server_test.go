package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/go-ai-webtools/internal/storage"
	"github.com/JetManiack/go-ai-webtools/internal/tools/fetch"
	"github.com/JetManiack/go-ai-webtools/internal/tools/search"
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
		DB:       db,
		Fetcher:  fetch.New(2*time.Second, 1<<20),
		Searcher: search.New(searxngURL, 2*time.Second),
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

	var out FetchOutput
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
	if call.Status != storage.ToolCallStatusOK {
		t.Errorf("Status = %q, want ok", call.Status)
	}
	// The arguments have to be legible after the fact — that's the whole point
	// of keeping them.
	if !strings.Contains(call.Args, origin.URL) {
		t.Errorf("Args = %q, want it to contain the requested URL", call.Args)
	}
	if !strings.Contains(call.ResponsePreview, "the page body") {
		t.Errorf("ResponsePreview = %q, want it to contain the response", call.ResponsePreview)
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

	var out SearchOutput
	structuredInto(t, result, &out)
	if len(out.Results) != 1 || out.Results[0].URL != "https://go.dev" {
		t.Errorf("Results = %+v, want the single hit from SearXNG", out.Results)
	}

	call := waitForOneHistoryRow(t, db)
	if call.Tool != "search" {
		t.Errorf("Tool = %q, want search", call.Tool)
	}
	if !strings.Contains(call.Args, "go mcp") {
		t.Errorf("Args = %q, want it to contain the query", call.Args)
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
	if call.Status != storage.ToolCallStatusError {
		t.Errorf("Status = %q, want error", call.Status)
	}
	if call.ErrorMessage == "" {
		t.Error("ErrorMessage is empty")
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
