// Package auth turns HTTP requests into core principals and selects the
// authorization policy for each auth mode.
package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aahoughton/smoot/internal/config"
	"github.com/aahoughton/smoot/internal/core"
)

// Authenticator identifies the caller of a request. It returns an error
// (normally a core.Error) when the request carries bad or missing credentials.
type Authenticator interface {
	Authenticate(r *http.Request) (core.Principal, error)
}

// None treats every request as the anonymous principal.
type None struct{}

// Authenticate always returns the anonymous principal.
func (None) Authenticate(*http.Request) (core.Principal, error) { return core.Anonymous(), nil }

// Bearer authenticates `Authorization: Bearer <token>`. The JSON API and
// MCP use it in auth mode token; browsers use session cookies instead.
type Bearer struct {
	Service *core.Service
}

// Authenticate implements Authenticator.
func (b Bearer) Authenticate(r *http.Request) (core.Principal, error) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return core.Principal{}, &core.Error{
			Code:    core.CodeUnauthorized,
			Message: "send Authorization: Bearer <token> (smoot clients read --token or SMOOT_TOKEN)",
		}
	}
	return b.Service.AuthenticateToken(r.Context(), strings.TrimSpace(token))
}

// SessionCookie holds a browser session secret in auth mode token.
const SessionCookie = "smoot_session"

// Session authenticates the HTML viewer's session cookie. Browsers get the
// cookie by pasting a token at the viewer's login page; the JSON API and
// MCP never accept it.
type Session struct {
	Service *core.Service
}

// Authenticate implements Authenticator.
func (s Session) Authenticate(r *http.Request) (core.Principal, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return core.Principal{}, &core.Error{Code: core.CodeUnauthorized, Message: "sign in"}
	}
	return s.Service.AuthenticateSession(r.Context(), c.Value)
}

// PolicyFor returns the core policy for an auth mode.
func PolicyFor(mode config.AuthMode) (core.Policy, error) {
	switch mode {
	case config.AuthNone:
		return core.AllowAll{}, nil
	case config.AuthToken:
		return core.RolePolicy(), nil
	default:
		return nil, fmt.Errorf("auth: mode %q is not implemented", mode)
	}
}

// For returns the authenticator for the JSON API and MCP in an auth mode,
// over a Service built with PolicyFor's policy.
func For(mode config.AuthMode, svc *core.Service) (Authenticator, error) {
	switch mode {
	case config.AuthNone:
		return None{}, nil
	case config.AuthToken:
		return Bearer{Service: svc}, nil
	default:
		return nil, fmt.Errorf("auth: mode %q is not implemented", mode)
	}
}
