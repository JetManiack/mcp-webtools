package restapi

import (
	"errors"
	"net/http"

	"github.com/JetManiack/mcp-webtools/internal/humanauth"
)

type meResponse struct {
	ActorID     string `json:"actor_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func meHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := humanauth.ActorFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("no authenticated actor"))
			return
		}
		role, _ := humanauth.RoleFromContext(r.Context())

		writeJSON(w, http.StatusOK, meResponse{
			ActorID:     actor.ID,
			DisplayName: actor.Name,
			Role:        role,
		})
	}
}
