package restapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/JetManiack/go-ai-webtools/internal/storage"
)

func TestCreateAndListAgents(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("admin"))

	resp := do(t, server, http.MethodPost, "/api/agents", `{"display_name":"scraper-1"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var created storage.Actor
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("created agent has no ID")
	}
	if created.Kind != storage.ActorKindAgent {
		t.Errorf("Kind = %q, want agent", created.Kind)
	}

	resp = do(t, server, http.MethodGet, "/api/agents", "")
	var agents []agentResponse
	decode(t, resp, &agents)
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].HasActiveToken {
		t.Error("a freshly created agent reports an active token")
	}
}

func TestCreateAgentRejectsBadInput(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("admin"))

	tests := []struct {
		name string
		body string
	}{
		{name: "empty name", body: `{"display_name":""}`},
		{name: "whitespace name", body: `{"display_name":"   "}`},
		{name: "malformed JSON", body: `{"display_name":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := do(t, server, http.MethodPost, "/api/agents", tt.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestIssueTokenIsReturnedOnceAndWorks(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	server := newTestAPI(t, db, providerWithRole("admin"))

	resp := do(t, server, http.MethodPost, "/api/agents/"+agent.ID+"/tokens", "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var issued issueTokenResponse
	decode(t, resp, &issued)
	if !strings.HasPrefix(issued.Token, storage.TokenPrefix) {
		t.Errorf("token = %q, want the %q prefix", issued.Token, storage.TokenPrefix)
	}

	// The token the API just handed out has to actually authenticate — the one
	// thing an issue endpoint must get right.
	if _, err := storage.AuthenticateAgentToken(db, issued.Token); err != nil {
		t.Errorf("the issued token does not authenticate: %v", err)
	}

	// Listing tokens must never expose the hash (let alone the token).
	resp = do(t, server, http.MethodGet, "/api/agents/"+agent.ID+"/tokens", "")
	body := readBody(t, resp)
	if strings.Contains(body, issued.Token) {
		t.Error("the token list echoes the raw token back")
	}
	if strings.Contains(body, "token_hash") || strings.Contains(body, "TokenHash") {
		t.Errorf("the token list exposes the stored hash: %s", body)
	}
}

func TestRevokeSingleToken(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	server := newTestAPI(t, db, providerWithRole("admin"))

	// Issue the first token and note its credential ID while it's the only one,
	// so the revocation below targets a known token rather than an arbitrary
	// row.
	resp := do(t, server, http.MethodPost, "/api/agents/"+agent.ID+"/tokens", "")
	var first issueTokenResponse
	decode(t, resp, &first)

	creds, err := storage.ListAgentCredentials(db, agent.ID)
	if err != nil {
		t.Fatalf("ListAgentCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("got %d credentials, want 1", len(creds))
	}
	firstCredID := creds[0].ID

	resp = do(t, server, http.MethodPost, "/api/agents/"+agent.ID+"/tokens", "")
	var second issueTokenResponse
	decode(t, resp, &second)

	resp = do(t, server, http.MethodDelete, "/api/agents/"+agent.ID+"/tokens/"+firstCredID, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	if _, err := storage.AuthenticateAgentToken(db, first.Token); err == nil {
		t.Error("the revoked token still authenticates")
	}
	// Revocation must be per-token, not per-agent: an agent rotating one
	// credential must not lose the others.
	if _, err := storage.AuthenticateAgentToken(db, second.Token); err != nil {
		t.Errorf("the other token was revoked too: %v", err)
	}
}

func TestDeleteAgentRevokesTokensButKeepsTheActor(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	server := newTestAPI(t, db, providerWithRole("admin"))

	resp := do(t, server, http.MethodPost, "/api/agents/"+agent.ID+"/tokens", "")
	var issued issueTokenResponse
	decode(t, resp, &issued)

	resp = do(t, server, http.MethodDelete, "/api/agents/"+agent.ID, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	if _, err := storage.AuthenticateAgentToken(db, issued.Token); err == nil {
		t.Error("the token still authenticates after the agent was deleted")
	}

	// The agent stays listed, flagged as having no active token — its history
	// still needs a named owner.
	resp = do(t, server, http.MethodGet, "/api/agents", "")
	var agents []agentResponse
	decode(t, resp, &agents)
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want the deleted agent to still be listed", len(agents))
	}
	if agents[0].HasActiveToken {
		t.Error("HasActiveToken = true after revoking every token")
	}
}

func TestListAgentTokensEmptyIsArray(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	server := newTestAPI(t, db, providerWithRole("admin"))

	resp := do(t, server, http.MethodGet, "/api/agents/"+agent.ID+"/tokens", "")
	var creds []storage.AgentCredential
	decode(t, resp, &creds)
	if creds == nil {
		t.Error("tokens = null, want []")
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
