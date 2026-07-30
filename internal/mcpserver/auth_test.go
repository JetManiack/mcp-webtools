package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JetManiack/go-ai-webtools/internal/storage"
)

// actorEcho reports whether the wrapped handler saw an authenticated actor.
func actorEcho(t *testing.T, want *storage.Actor) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			t.Error("handler ran without an actor in context")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if actor.ID != want.ID {
			t.Errorf("actor = %q, want %q", actor.ID, want.ID)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireAgentTokenAcceptsValidToken(t *testing.T) {
	db := openTestDB(t)
	agent, token := mustAgentWithToken(t, db, "scraper-1")

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	RequireAgentToken(db, actorEcho(t, agent)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRequireAgentTokenRejects(t *testing.T) {
	db := openTestDB(t)
	_, valid := mustAgentWithToken(t, db, "scraper-1")

	revoked, revokedToken := mustAgentWithToken(t, db, "scraper-2")
	if err := storage.RevokeAllAgentCredentials(db, revoked.ID); err != nil {
		t.Fatalf("RevokeAllAgentCredentials: %v", err)
	}

	tests := []struct {
		name   string
		header string
	}{
		{name: "no header", header: ""},
		{name: "bearer with no token", header: "Bearer "},
		{name: "wrong scheme", header: "Basic " + valid},
		{name: "token without scheme", header: valid},
		{name: "lowercase scheme", header: "bearer " + valid},
		{name: "unknown token", header: "Bearer wt_definitely-not-a-token"},
		{name: "revoked token", header: "Bearer " + revokedToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			RequireAgentToken(db, next).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if called {
				t.Error("the wrapped handler ran despite failed authentication")
			}
			// A 401 without WWW-Authenticate leaves a client with no idea what
			// kind of credential to present.
			if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
			}
		})
	}
}

func TestActorFromContextWithoutActor(t *testing.T) {
	if _, ok := ActorFromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Error("ActorFromContext reported an actor on a bare context")
	}
}
