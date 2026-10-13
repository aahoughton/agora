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
	account int64
	seen    time.Time
}

// ownerMargin keeps an owner entry past the SDK's own session timeout, so
// the SDK always forgets a session before the guard does.
const ownerMargin = time.Hour

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := g.authn.Authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if sid := r.Header.Get(sessionHeader); sid != "" {
		switch g.check(sid, p) {
		case sessionUnknown:
			// The SDK may still hold a session the guard has forgotten;
			// refusing it keeps any other caller from taking it over. The
			// client starts a new session on 404.
			writeError(w, http.StatusNotFound, "session_not_found", "unknown MCP session; initialize a new one")
			return
		case sessionOther:
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

type sessionCheck int

const (
	sessionMine sessionCheck = iota
	sessionUnknown
	sessionOther
)

// check reports whether p may use sid. A session belongs to an account, not
// to the credential that created it, so another valid token of the same
// account may continue it.
func (g *guard) check(sid string, p core.Principal) sessionCheck {
	g.mu.Lock()
	defer g.mu.Unlock()
	o, ok := g.owners[sid]
	switch {
	case !ok:
		return sessionUnknown
	case o.account != p.AccountID:
		return sessionOther
	}
	o.seen = time.Now()
	g.owners[sid] = o
	return sessionMine
}

func (g *guard) record(sid string, p core.Principal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for id, o := range g.owners {
		if now.Sub(o.seen) > g.ttl+ownerMargin {
			delete(g.owners, id)
		}
	}
	g.owners[sid] = owner{account: p.AccountID, seen: now}
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
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agora"`)
	}
	writeError(w, status, code, msg)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
