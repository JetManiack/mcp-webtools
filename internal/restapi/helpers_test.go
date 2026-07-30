package restapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/JetManiack/go-ai-webtools/internal/humanauth"
	"github.com/JetManiack/go-ai-webtools/internal/storage"
)

// fakeProvider stands in for the OIDC provider so tests can pick the role of
// the caller — or make authentication fail outright — without a Keycloak.
type fakeProvider struct {
	identity *humanauth.Identity
	err      error
}

func (f fakeProvider) Authenticate(*http.Request) (*humanauth.Identity, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.identity, nil
}

func providerWithRole(role string) fakeProvider {
	return fakeProvider{identity: &humanauth.Identity{
		Subject:     "subject-" + role,
		DisplayName: "Tester (" + role + ")",
		Role:        role,
	}}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	return db
}

// newTestAPI mounts the REST handler the way cmd/webtools does — under /api,
// with the prefix stripped — so route paths in tests match the real ones.
func newTestAPI(t *testing.T, db *gorm.DB, provider humanauth.Provider) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", NewHandler(db, provider)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func do(t *testing.T, server *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func mustAgent(t *testing.T, db *gorm.DB, name string) *storage.Actor {
	t.Helper()
	agent, err := storage.CreateAgent(db, name)
	if err != nil {
		t.Fatalf("CreateAgent(%q): %v", name, err)
	}
	return agent
}

func recordCall(t *testing.T, db *gorm.DB, actorID, tool string, status storage.ToolCallStatus, at time.Time) *storage.ToolCall {
	t.Helper()
	call := &storage.ToolCall{
		ActorID:   actorID,
		Tool:      tool,
		Args:      fmt.Sprintf(`{"url":"https://example.com/%d"}`, at.UnixNano()),
		Status:    status,
		CreatedAt: at,
	}
	if status == storage.ToolCallStatusError {
		call.ErrorMessage = "boom"
	} else {
		call.ResponsePreview = `{"content":"hi"}`
		call.ResponseBytes = 16
	}
	if err := storage.RecordToolCall(db, call); err != nil {
		t.Fatalf("RecordToolCall: %v", err)
	}
	return call
}

var errNoSession = errors.New("no session cookie")
