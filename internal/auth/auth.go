// Package auth authenticates AI agent requests to the MCP surface.
package auth

import (
	"context"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/storage"
)

type contextKey string

const actorContextKey contextKey = "actor"

func withActor(ctx context.Context, actor *storage.Actor) context.Context {
	return context.WithValue(ctx, actorContextKey, actor)
}

// ActorFromContext returns the Actor authenticated by RequireBearer for the current request.
func ActorFromContext(ctx context.Context) (*storage.Actor, bool) {
	actor, ok := ctx.Value(actorContextKey).(*storage.Actor)
	return actor, ok
}

// WithActorForTesting injects actor into ctx the same way RequireBearer does.
func WithActorForTesting(ctx context.Context, actor *storage.Actor) context.Context {
	return withActor(ctx, actor)
}

// Authenticate verifies raw bearer token and returns the owning Actor.
func Authenticate(db *gorm.DB, raw string) (*storage.Actor, error) {
	return storage.AuthenticateAgentToken(db, raw)
}

// Issue generates a new bearer token for actorID, optionally labeled.
// The label is stored for display; the raw token is returned once and never stored.
func Issue(db *gorm.DB, actorID, label string) (string, error) {
	return storage.IssueAgentToken(db, actorID)
}

// RequireBearer authenticates every request by its Authorization: Bearer header
// and injects the resulting Actor into the request context.
func RequireBearer(db *gorm.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="webtools"`)
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}

		actor, err := Authenticate(db, token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="webtools", error="invalid_token"`)
			http.Error(w, "invalid or revoked token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(withActor(r.Context(), actor)))
	})
}
