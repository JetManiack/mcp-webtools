package storage

import (
	"errors"
	"strings"
	"testing"
)

func TestCreateAgent(t *testing.T) {
	db := openTestDB(t)

	agent, err := CreateAgent(db, "scraper-1")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if agent.ID == "" {
		t.Error("ID is empty")
	}
	if agent.Kind != ActorKindAgent {
		t.Errorf("Kind = %q, want %q", agent.Kind, ActorKindAgent)
	}
	if agent.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
}

func TestCreateAgentRejectsEmptyName(t *testing.T) {
	db := openTestDB(t)

	for _, name := range []string{"", "   ", "\t\n"} {
		if _, err := CreateAgent(db, name); !errors.Is(err, ErrEmptyDisplayName) {
			t.Errorf("CreateAgent(%q) error = %v, want ErrEmptyDisplayName", name, err)
		}
	}
}

func TestIssueAndAuthenticateToken(t *testing.T) {
	db := openTestDB(t)
	agent, err := CreateAgent(db, "scraper-1")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	raw, err := IssueAgentToken(db, agent.ID)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) {
		t.Errorf("token %q does not start with %q", raw, TokenPrefix)
	}

	// The raw token must never be persisted — only its hash. A database dump
	// must not be a set of working credentials.
	var stored AgentCredential
	if err := db.First(&stored, "actor_id = ?", agent.ID).Error; err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if stored.TokenHash == raw {
		t.Error("the raw token was stored verbatim")
	}
	if strings.Contains(stored.TokenHash, strings.TrimPrefix(raw, TokenPrefix)) {
		t.Error("the stored hash contains the raw token")
	}

	authenticated, err := AuthenticateAgentToken(db, raw)
	if err != nil {
		t.Fatalf("AuthenticateAgentToken: %v", err)
	}
	if authenticated.ID != agent.ID {
		t.Errorf("authenticated actor = %q, want %q", authenticated.ID, agent.ID)
	}

	// last_used_at is what tells an operator a credential is actually in use.
	var afterUse AgentCredential
	if err := db.First(&afterUse, "id = ?", stored.ID).Error; err != nil {
		t.Fatalf("reload credential: %v", err)
	}
	if afterUse.LastUsedAt == nil {
		t.Error("LastUsedAt was not updated on a successful authentication")
	}
}

func TestIssueAgentTokenProducesDistinctTokens(t *testing.T) {
	db := openTestDB(t)
	agent, _ := CreateAgent(db, "scraper-1")

	seen := make(map[string]bool)
	for range 5 {
		raw, err := IssueAgentToken(db, agent.ID)
		if err != nil {
			t.Fatalf("IssueAgentToken: %v", err)
		}
		if seen[raw] {
			t.Fatalf("token %q was issued twice", raw)
		}
		seen[raw] = true
	}
}

func TestAuthenticateRejectsBadTokens(t *testing.T) {
	db := openTestDB(t)
	agent, _ := CreateAgent(db, "scraper-1")
	raw, _ := IssueAgentToken(db, agent.ID)

	tests := []struct {
		name  string
		token string
	}{
		{name: "unknown", token: TokenPrefix + "nope"},
		{name: "empty", token: ""},
		{name: "prefix only", token: TokenPrefix},
		{name: "truncated", token: raw[:len(raw)-3]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := AuthenticateAgentToken(db, tt.token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestRevokeAgentToken(t *testing.T) {
	db := openTestDB(t)
	agent, _ := CreateAgent(db, "scraper-1")
	raw, _ := IssueAgentToken(db, agent.ID)

	creds, err := ListAgentCredentials(db, agent.ID)
	if err != nil {
		t.Fatalf("ListAgentCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("got %d credentials, want 1", len(creds))
	}

	if err := RevokeAgentToken(db, creds[0].ID); err != nil {
		t.Fatalf("RevokeAgentToken: %v", err)
	}
	if _, err := AuthenticateAgentToken(db, raw); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a revoked token still authenticates: %v", err)
	}

	// Revoking twice, or revoking something that doesn't exist, is a no-op —
	// a revocation endpoint that errors on an already-revoked token makes
	// "make sure this is revoked" needlessly awkward.
	if err := RevokeAgentToken(db, creds[0].ID); err != nil {
		t.Errorf("second RevokeAgentToken: %v", err)
	}
	if err := RevokeAgentToken(db, "no-such-credential"); err != nil {
		t.Errorf("RevokeAgentToken(unknown): %v", err)
	}
}

func TestRevokeAllAgentCredentials(t *testing.T) {
	db := openTestDB(t)
	agent, _ := CreateAgent(db, "scraper-1")
	other, _ := CreateAgent(db, "scraper-2")

	var agentTokens []string
	for range 3 {
		raw, _ := IssueAgentToken(db, agent.ID)
		agentTokens = append(agentTokens, raw)
	}
	otherToken, _ := IssueAgentToken(db, other.ID)

	if err := RevokeAllAgentCredentials(db, agent.ID); err != nil {
		t.Fatalf("RevokeAllAgentCredentials: %v", err)
	}

	for _, raw := range agentTokens {
		if _, err := AuthenticateAgentToken(db, raw); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("a revoked token still authenticates: %v", err)
		}
	}
	// Revocation must be scoped to the one agent.
	if _, err := AuthenticateAgentToken(db, otherToken); err != nil {
		t.Errorf("another agent's token was revoked too: %v", err)
	}

	// The Actor row survives, so history it produced keeps a named owner.
	var stillThere Actor
	if err := db.First(&stillThere, "id = ?", agent.ID).Error; err != nil {
		t.Errorf("the Actor row was deleted along with its credentials: %v", err)
	}
}

func TestListAgentsOnlyReturnsAgents(t *testing.T) {
	db := openTestDB(t)
	if _, err := CreateAgent(db, "scraper-1"); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "admin"); err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}

	agents, err := ListAgents(db)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1 (humans must not be listed as agents)", len(agents))
	}
	if agents[0].DisplayName != "scraper-1" {
		t.Errorf("agent = %q, want scraper-1", agents[0].DisplayName)
	}
}

func TestActorsWithActiveToken(t *testing.T) {
	db := openTestDB(t)
	withToken, _ := CreateAgent(db, "has-token")
	revoked, _ := CreateAgent(db, "revoked")
	never, _ := CreateAgent(db, "never-issued")

	if _, err := IssueAgentToken(db, withToken.ID); err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, err := IssueAgentToken(db, revoked.ID); err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if err := RevokeAllAgentCredentials(db, revoked.ID); err != nil {
		t.Fatalf("RevokeAllAgentCredentials: %v", err)
	}

	active, err := ActorsWithActiveToken(db)
	if err != nil {
		t.Fatalf("ActorsWithActiveToken: %v", err)
	}
	if !active[withToken.ID] {
		t.Error("an agent with a live token is not reported active")
	}
	if active[revoked.ID] {
		t.Error("an agent whose tokens are all revoked is reported active")
	}
	if active[never.ID] {
		t.Error("an agent that never had a token is reported active")
	}
}

func TestActorNamesByID(t *testing.T) {
	db := openTestDB(t)
	first, _ := CreateAgent(db, "scraper-1")
	second, _ := CreateAgent(db, "scraper-2")

	names, err := ActorNamesByID(db, []string{first.ID, second.ID, "missing-id"})
	if err != nil {
		t.Fatalf("ActorNamesByID: %v", err)
	}
	if names[first.ID] != "scraper-1" {
		t.Errorf("names[first] = %q, want scraper-1", names[first.ID])
	}
	if names[second.ID] != "scraper-2" {
		t.Errorf("names[second] = %q, want scraper-2", names[second.ID])
	}
	// An unknown ID must be absent rather than an error: history outlives the
	// actors it references.
	if _, ok := names["missing-id"]; ok {
		t.Error("an unknown ID produced an entry")
	}

	empty, err := ActorNamesByID(db, nil)
	if err != nil {
		t.Fatalf("ActorNamesByID(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ActorNamesByID(nil) = %v, want empty", empty)
	}
}
