package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
)

func TestForModeNone(t *testing.T) {
	authn, policy, err := ForMode(config.AuthNone)
	if err != nil {
		t.Fatal(err)
	}
	p, err := authn.Authenticate(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if err != nil || !p.IsAnonymous() {
		t.Fatalf("principal = %+v, err = %v", p, err)
	}
	if err := policy.Authorize(t.Context(), p, "default", core.ActionAdmin); err != nil {
		t.Fatal(err)
	}
}

func TestForModeUnknown(t *testing.T) {
	if _, _, err := ForMode("ldap"); err == nil {
		t.Fatal("want error")
	}
}
