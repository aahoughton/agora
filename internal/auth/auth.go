// Package auth turns HTTP requests into core principals and selects the
// authorization policy for each auth mode.
package auth

import (
	"fmt"
	"net/http"

	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
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

// ForMode returns the authenticator and policy for an auth mode.
func ForMode(mode config.AuthMode) (Authenticator, core.Policy, error) {
	switch mode {
	case config.AuthNone:
		return None{}, core.AllowAll{}, nil
	default:
		return nil, nil, fmt.Errorf("auth: mode %q is not implemented", mode)
	}
}
