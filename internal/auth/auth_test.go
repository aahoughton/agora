package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

func service(t *testing.T, mode config.AuthMode) *core.Service {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	policy, err := PolicyFor(mode)
	if err != nil {
		t.Fatal(err)
	}
	return core.New(db, core.Options{Policy: policy})
}

func request(t *testing.T, authorization string) *http.Request {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

func TestModeNone(t *testing.T) {
	svc := service(t, config.AuthNone)
	authn, err := For(config.AuthNone, svc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := authn.Authenticate(request(t, ""))
	if err != nil || !p.IsAnonymous() {
		t.Fatalf("principal = %+v, err = %v", p, err)
	}
	if _, err := svc.CreateNamespace(t.Context(), p, "anyone"); err != nil {
		t.Fatal(err)
	}
}

func TestModeToken(t *testing.T) {
	svc := service(t, config.AuthToken)
	authn, err := For(config.AuthToken, svc)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.Bootstrap(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"", "Basic abc", "Bearer ", "Bearer agora_nope", "Bearer x"} {
		if _, err := authn.Authenticate(request(t, h)); core.CodeOf(err) != core.CodeUnauthorized {
			t.Errorf("%q: err = %v", h, err)
		}
	}
	p, err := authn.Authenticate(request(t, "bearer  "+tok.Secret))
	if err != nil || p.IsAnonymous() {
		t.Fatalf("principal = %+v, err = %v", p, err)
	}
	if _, err := svc.CreateNamespace(t.Context(), p, "team"); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownMode(t *testing.T) {
	if _, err := PolicyFor("ldap"); err == nil {
		t.Fatal("want error")
	}
	if _, err := For("ldap", nil); err == nil {
		t.Fatal("want error")
	}
}
