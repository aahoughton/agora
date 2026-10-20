package api_test

import (
	"net/http"
	"testing"

	"github.com/aahoughton/smoot/internal/auth"
	"github.com/aahoughton/smoot/internal/core"
)

func TestReadAsAgentNeedsAnAgent(t *testing.T) {
	c := newServer(t, auth.None{}, core.Options{})
	c.ok("POST", "/v1/namespaces/default/conversations", map[string]any{"name": "c", "agent": "a"}, http.StatusCreated, nil)
	for _, body := range []string{`{}`, `null`, `{"agent":""}`} {
		c.wantError("POST", "/v1/namespaces/default/conversations/c/read", body, http.StatusBadRequest, "invalid")
	}
}
