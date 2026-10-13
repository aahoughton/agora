package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
)

const sessionHeader = "Mcp-Session-Id"

type principalKey struct{}

// guard authenticates every MCP request, not only the one that creates a
// session, and refuses requests on a session created by another principal.
type guard struct {
	authn      auth.Authenticator
	next       http.Handler
	ttl        time.Duration
	perAccount int
	total      int

	mu     sync.Mutex
	owners map[string]owner
	// starting counts sessions admitted but not yet recorded, per account;
	// they hold their place against the caps.
	starting map[int64]int
}

type owner struct {
	account int64
	seen    time.Time
	// srv is the session's server; once it has no session left, the SDK
	// has closed the session and the entry is pruned.
	srv *mcp.Server
}

// serverRef carries the server the SDK creates for a new session back to
// the guard.
type serverRef struct{ srv *mcp.Server }

type serverRefKey struct{}

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
	if err := g.admit(p); err != nil {
		writeError(w, http.StatusTooManyRequests, "too_many_sessions", err.Error())
		return
	}
	cw := &captureSession{ResponseWriter: w}
	ref := &serverRef{}
	ctx := context.WithValue(context.WithValue(r.Context(), principalKey{}, p), serverRefKey{}, ref)
	defer func() { g.record(cw.sid, p, ref.srv) }()
	g.next.ServeHTTP(cw, r.WithContext(ctx))
}

// admit refuses a new session past the per-account or total cap, after
// dropping entries for sessions the SDK has closed. An admitted session
// holds its place until record, so concurrent starts cannot pass a cap
// together.
func (g *guard) admit(p core.Principal) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune(time.Now())
	mine, all := g.starting[p.AccountID], 0
	for _, n := range g.starting {
		all += n
	}
	for _, o := range g.owners {
		if o.account == p.AccountID {
			mine++
		}
	}
	all += len(g.owners)
	switch {
	case mine >= g.perAccount:
		return fmt.Errorf("this account has %d open MCP sessions, the most allowed; end idle ones (they close after %s idle)", mine, g.ttl)
	case all >= g.total:
		return fmt.Errorf("the server has %d open MCP sessions, the most allowed; retry later", all)
	}
	if g.starting == nil {
		g.starting = map[int64]int{}
	}
	g.starting[p.AccountID]++
	return nil
}

// prune drops sessions the SDK has closed and entries well past the idle
// timeout. The caller holds g.mu.
func (g *guard) prune(now time.Time) {
	for id, o := range g.owners {
		if now.Sub(o.seen) > g.ttl+ownerMargin || (o.srv != nil && !hasSession(o.srv)) {
			delete(g.owners, id)
		}
	}
}

func hasSession(s *mcp.Server) bool {
	for range s.Sessions() {
		return true
	}
	return false
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

// record ends an admitted start: the session it created, if any, now
// counts in its place.
func (g *guard) record(sid string, p core.Principal, srv *mcp.Server) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.starting[p.AccountID]--; g.starting[p.AccountID] <= 0 {
		delete(g.starting, p.AccountID)
	}
	if sid == "" {
		return
	}
	now := time.Now()
	g.prune(now)
	g.owners[sid] = owner{account: p.AccountID, seen: now, srv: srv}
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
		switch ce.Code {
		case core.CodeForbidden:
			status = http.StatusForbidden
		case core.CodeRateLimited:
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", strconv.Itoa(int(ce.RetryAfter.Seconds())))
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
