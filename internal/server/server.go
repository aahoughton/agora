// Package server assembles the HTTP handler for `agora serve`.
package server

import (
	"encoding/json"
	"net/http"
)

// New returns the root handler. Routes are added here as the API, MCP and
// HTML surfaces land.
func New() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
