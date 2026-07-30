package restapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/JetManiack/go-ai-webtools/internal/humanauth"
)

// NewHandler builds the full REST API handler, mounted with no path prefix
// (the caller mounts it under /api — see cmd/webtools/main.go). Every route
// requires human authentication via provider; agent management additionally
// requires role admin, while history is readable by any authenticated human.
func NewHandler(db *gorm.DB, provider humanauth.Provider) http.Handler {
	r := chi.NewRouter()
	r.Use(humanauth.RequireHumanAuth(db, provider))

	r.Route("/history", func(r chi.Router) {
		r.Get("/", listHistoryHandler(db))
		r.Get("/tools", listHistoryToolsHandler(db))
		r.Get("/{id}", getHistoryEntryHandler(db))
	})

	r.Route("/agents", func(r chi.Router) {
		r.Use(humanauth.RequireAdmin)
		r.Get("/", listAgentsHandler(db))
		r.Post("/", createAgentHandler(db))
		r.Delete("/{id}", deleteAgentHandler(db))
		r.Get("/{id}/tokens", listAgentTokensHandler(db))
		r.Post("/{id}/tokens", issueTokenHandler(db))
		r.Delete("/{id}/tokens/{tokenID}", revokeTokenHandler(db))
	})

	r.Get("/me", meHandler())

	return r
}
