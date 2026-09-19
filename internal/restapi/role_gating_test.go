package restapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/JetManiack/mcp-webtools/internal/storage"
)

// A viewer can read history and nothing else. Agent management is where tokens
// are minted, so it has to stay admin-only — and the check belongs on the
// server, not in the UI that merely hides the tab.
func TestViewerCannotManageAgents(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	server := newTestAPI(t, db, providerWithRole("viewer"))

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list agents", method: http.MethodGet, path: "/api/actors"},
		{name: "create agent", method: http.MethodPost, path: "/api/actors", body: `{"display_name":"nope"}`},
		{name: "delete agent", method: http.MethodDelete, path: "/api/actors/" + agent.ID},
		{name: "list tokens", method: http.MethodGet, path: "/api/actors/" + agent.ID + "/credentials"},
		{name: "issue token", method: http.MethodPost, path: "/api/actors/" + agent.ID + "/credentials"},
		{name: "revoke token", method: http.MethodDelete, path: "/api/credentials/whatever"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := do(t, server, tt.method, tt.path, tt.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}

	// No token may have been created by any of the above.
	creds, err := storage.ListAgentCredentials(db, agent.ID)
	if err != nil {
		t.Fatalf("ListAgentCredentials: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("%d credentials exist, want 0 — a viewer minted a token", len(creds))
	}
}

func TestViewerCanReadHistory(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	call := recordCall(t, db, agent.ID, "fetch", false, time.Now().UTC())
	server := newTestAPI(t, db, providerWithRole("viewer"))

	for _, path := range []string{"/api/tool-calls", "/api/tool-calls/tools", "/api/tool-calls/" + call.ID, "/api/me"} {
		resp := do(t, server, http.MethodGet, path, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestAdminCanManageAgents(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("admin"))

	resp := do(t, server, http.MethodGet, "/api/actors", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// An unrecognized role is not an admin. Defaulting the other way would turn any
// future role added to Keycloak into a privilege escalation.
func TestUnknownRoleIsNotAdmin(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("auditor"))

	resp := do(t, server, http.MethodGet, "/api/actors", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
