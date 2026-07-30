// Package restapi serves the human-facing REST API, mounted under /api.
package restapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeError writes err as a JSON error response. For 5xx statuses, the raw
// error is logged server-side but never sent to the client — reflecting
// internal error strings (DB failures, DSNs) into response bodies is an
// information leak. 4xx statuses keep their specific message: those are
// client-input errors (e.g. "unknown pagination cursor"), safe and useful to
// show as-is.
func writeError(w http.ResponseWriter, status int, err error) {
	if status >= 500 {
		slog.Error("internal error", "status", status, "error", err)
		writeJSON(w, status, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
