package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
)

const sessionHeader = "Mcp-Session-Id"

type principalKey struct{}

// guard authenticates every MCP request, not only the one that creates a
// session, and refuses requests on a session created by another principal.
type guard struct {
	authn auth.Authenticator
	next  http.Handler
	ttl   time.Duration

	mu     sync.Mutex
	owners map[string]owner
}

type owner struct {
	p    core.Principal
	seen time.Time
}

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := g.authn.Authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if sid := r.Header.Get(sessionHeader); sid != "" {
		if !g.owns(sid, p) {
			writeAuthError(w, &core.Error{Code: core.CodeForbidden, Message: "this MCP session belongs to another caller"})
			return
		}
		if r.Method == http.MethodDelete {
			defer g.forget(sid)
		}
		g.next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		return
	}
	cw := &captureSession{ResponseWriter: w}
	g.next.ServeHTTP(cw, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	if cw.sid != "" {
		g.record(cw.sid, p)
	}
}

// owns reports whether p may use sid. Unknown sessions pass through so the
// SDK can answer them (normally 404 after a restart).
func (g *guard) owns(sid string, p core.Principal) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	o, ok := g.owners[sid]
	if !ok {
		return true
	}
	if o.p != p {
		return false
	}
	o.seen = time.Now()
	g.owners[sid] = o
	return true
}

func (g *guard) record(sid string, p core.Principal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for id, o := range g.owners {
		if now.Sub(o.seen) > g.ttl {
			delete(g.owners, id)
		}
	}
	g.owners[sid] = owner{p: p, seen: now}
}

func (g *guard) forget(sid string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.owners, sid)
}

// captureSession records the session id the SDK assigns on initialize.
type captureSession struct {
	http.ResponseWriter
	sid string
}

func (c *captureSession) WriteHeader(code int) {
	c.sid = c.Header().Get(sessionHeader)
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureSession) Write(b []byte) (int, error) {
	if c.sid == "" {
		c.sid = c.Header().Get(sessionHeader)
	}
	return c.ResponseWriter.Write(b)
}

// Flush keeps streaming (SSE) responses working through the wrapper.
func (c *captureSession) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *captureSession) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func writeAuthError(w http.ResponseWriter, err error) {
	status, code, msg := http.StatusUnauthorized, "unauthorized", "authentication failed"
	if ce, ok := errors.AsType[*core.Error](err); ok {
		code, msg = string(ce.Code), ce.Message
		if ce.Code == core.CodeForbidden {
			status = http.StatusForbidden
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
