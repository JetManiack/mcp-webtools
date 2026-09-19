package restapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/auth"
	"github.com/JetManiack/mcp-webtools/internal/storage"
)

type createAgentRequest struct {
	DisplayName string `json:"display_name"`
}

type issueTokenResponse struct {
	Token string `json:"token"`
}

// agentResponse embeds storage.Actor and adds HasActiveToken so clients can
// tell an agent with no usable credential from one that can still
// authenticate — DELETE /agents/{id} revokes credentials rather than removing
// the Actor row, so revoked agents remain in this list (and their history
// stays attributable).
type agentResponse struct {
	storage.Actor
	HasActiveToken bool `json:"has_active_token"`
}

func listAgentsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agents, err := storage.ListAgents(db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		active, err := storage.ActorsWithActiveToken(db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		resp := make([]agentResponse, 0, len(agents))
		for _, agent := range agents {
			resp = append(resp, agentResponse{Actor: agent, HasActiveToken: active[agent.ID]})
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func createAgentHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("body must be a JSON object with display_name"))
			return
		}

		agent, err := storage.CreateAgent(db, req.DisplayName)
		if errors.Is(err, storage.ErrEmptyDisplayName) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, agent)
	}
}

// deleteAgentHandler revokes every credential belonging to the agent rather
// than deleting its Actor row: the agent can no longer authenticate, and the
// tool-call history it already produced keeps a named owner instead of
// pointing at a vanished actor.
func deleteAgentHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := storage.RevokeAllAgentCredentials(db, chi.URLParam(r, "id")); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listAgentTokensHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		creds, err := storage.ListAgentCredentials(db, chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if creds == nil {
			creds = []storage.AgentCredential{}
		}
		writeJSON(w, http.StatusOK, creds)
	}
}

func issueTokenHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := auth.Issue(db, chi.URLParam(r, "id"), "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, issueTokenResponse{Token: token})
	}
}

func revokeTokenHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := storage.RevokeAgentToken(db, chi.URLParam(r, "id")); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
