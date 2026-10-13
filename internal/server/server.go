// Package server assembles the HTTP handler for `agora serve`.
package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/aahoughton/agora/internal/api"
	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/mcpserver"
)

// Deps are what the handler serves.
type Deps struct {
	Service       *core.Service
	Authenticator auth.Authenticator
	Logger        *slog.Logger
	MCPMaxWait    time.Duration
	Version       string
	// Web, if set, serves the HTML viewer at every path the API, MCP and
	// health routes do not claim.
	Web http.Handler
	// AllowedHosts, if non-empty, refuses requests whose Host is not one of
	// these names or addresses (port ignored, case-insensitive).
	AllowedHosts []string
}

// New returns the root handler: GET /healthz, the /v1 JSON API, and MCP at
// /mcp. Browser
// cross-origin writes are refused everywhere, so a web page cannot post into
// a local server; with AllowedHosts, so are requests that name another host,
// which blocks DNS rebinding.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	api.Register(mux, d.Service, d.Authenticator, api.Options{Logger: d.Logger})
	mux.Handle("/mcp", mcpserver.Handler(d.Service, d.Authenticator, mcpserver.Options{
		Logger: d.Logger, MaxWait: d.MCPMaxWait, Version: d.Version,
	}))
	if d.Web != nil {
		mux.Handle("/", d.Web)
	}
	h := http.NewCrossOriginProtection().Handler(mux)
	if len(d.AllowedHosts) > 0 {
		h = allowHosts(d.AllowedHosts, h)
	}
	if d.Logger != nil {
		h = logRequests(d.Logger, h)
	}
	return h
}

// LoopbackHosts are the names a loopback-only server answers to.
var LoopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

func allowHosts(allowed []string, next http.Handler) http.Handler {
	set := map[string]bool{}
	for _, a := range allowed {
		set[strings.ToLower(strings.Trim(a, "[]"))] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !set[strings.ToLower(strings.Trim(host, "[]"))] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
				"code":    "forbidden",
				"message": "host " + host + " is not in this server's allowed hosts (--allowed-hosts)",
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}
