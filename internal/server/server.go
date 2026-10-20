// Package server assembles the HTTP handler for `smoot serve`.
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/aahoughton/smoot/internal/api"
	"github.com/aahoughton/smoot/internal/auth"
	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/mcpserver"
)

// Deps are what the handler serves.
type Deps struct {
	Service       *core.Service
	Authenticator auth.Authenticator
	Logger        *slog.Logger
	MCPMaxWait    time.Duration
	Version       string
	// AuthMode is reported by GET /v1/whoami.
	AuthMode string
	// Web, if set, serves the HTML viewer at every path the API, MCP and
	// health routes do not claim.
	Web http.Handler
	// AllowedHosts, if non-empty, refuses requests whose Host is not one of
	// these names or addresses (port ignored, case-insensitive).
	AllowedHosts []string
	// BodyReadTimeout bounds how long a client may take to send a request
	// body, on every route; default api.DefaultBodyReadTimeout.
	BodyReadTimeout time.Duration
	// Activity, if set, records the callers of the API and MCP.
	Activity *auth.Activity
	// PublicURL, if set, is the URL clients reach the server at; the MCP
	// instructions quote it instead of the URL a request arrived on.
	PublicURL string
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
	mcp := mcpserver.Handler(d.Service, d.Authenticator, mcpserver.Options{
		Logger: d.Logger, MaxWait: d.MCPMaxWait, Version: d.Version, Activity: d.Activity, URL: d.PublicURL,
	})
	api.Register(mux, d.Service, d.Authenticator, api.Options{
		Logger: d.Logger, AuthMode: d.AuthMode, Version: d.Version,
		Activity: d.Activity, MCPSessions: mcp.Sessions,
	})
	mux.Handle("/mcp", mcp)
	if d.Web != nil {
		mux.Handle("/", d.Web)
	}
	h := http.NewCrossOriginProtection().Handler(mux)
	if len(d.AllowedHosts) > 0 {
		h = allowHosts(d.AllowedHosts, h)
	}
	timeout := d.BodyReadTimeout
	if timeout == 0 {
		timeout = api.DefaultBodyReadTimeout
	}
	h = bodyDeadline(timeout, h)
	if d.Logger != nil {
		h = logRequests(d.Logger, h)
	}
	return h
}

// bodyDeadline gives a request that carries a body a deadline to finish
// sending it, so a stalled client cannot hold a connection open. Responses,
// such as long polls and event streams, are not bounded by it.
func bodyDeadline(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(timeout)) // unsupported writers keep no deadline
			r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
		}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody lifts the read deadline once the body has been read, so the
// connection can be kept alive for the next request.
type deadlineBody struct {
	io.ReadCloser
	rc *http.ResponseController
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
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
