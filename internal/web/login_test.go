package web_test

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
	"github.com/aahoughton/agora/internal/web"
)

// tokenEnv serves the viewer in auth mode token, with a member account
// "pat" and its token.
type tokenEnv struct {
	*env
	token string
}

func newTokenEnv(t *testing.T, opts web.Options) *tokenEnv {
	t.Helper()
	return newTokenEnvWith(t, opts, core.Options{})
}

func newTokenEnvWith(t *testing.T, opts web.Options, coreOpts core.Options) *tokenEnv {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	coreOpts.Policy = core.RolePolicy()
	svc := core.New(db, coreOpts)
	root, err := svc.Bootstrap(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := svc.AuthenticateToken(t.Context(), root.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAccount(t.Context(), admin, "pat", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetMembership(t.Context(), admin, "default", "pat", core.RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(t.Context(), admin, "default", core.StartParams{Name: "c", Agent: "boss"}); err != nil {
		t.Fatal(err)
	}
	tok, err := svc.IssueToken(t.Context(), admin, "pat", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	opts.Login = true
	opts.Version = "test"
	h, err := web.Handler(svc, auth.Session{Service: svc}, opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &tokenEnv{env: &env{t: t, svc: svc, url: srv.URL, c: c}, token: tok.Secret}
}

// reply is a response's status and headers; its body is already closed.
type reply struct {
	StatusCode int
	Header     http.Header
}

// postRaw posts a form and returns the response's status and headers.
func (e *tokenEnv) postRaw(path string, form url.Values) reply {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodPost, e.url+path, strings.NewReader(form.Encode()))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return reply{resp.StatusCode, resp.Header}
}

func (e *tokenEnv) sessionCookie() *http.Cookie {
	u, _ := url.Parse(e.url)
	for _, c := range e.c.Jar.Cookies(u) {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	return nil
}

func TestLogin(t *testing.T) {
	e := newTokenEnv(t, web.Options{})

	// Signed out: pages send the browser to the login page; streams 401.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.url+"/n/default/c/c", nil)
	first, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()
	if first.StatusCode != http.StatusSeeOther || first.Header.Get("Location") != "/login?next=%2Fn%2Fdefault%2Fc%2Fc" {
		t.Fatalf("signed out page = %d %s", first.StatusCode, first.Header.Get("Location"))
	}
	if code, _ := e.get("/n/default/c/c/events"); code != http.StatusUnauthorized {
		t.Fatalf("signed out stream = %d", code)
	}
	code, body := e.get("/login?next=/n/default")
	if code != http.StatusOK || !strings.Contains(body, `type="password"`) || !strings.Contains(body, `value="/n/default"`) {
		t.Fatalf("login page = %d %s", code, body)
	}

	// A bad token is refused without a cookie.
	resp := e.postRaw("/login", url.Values{"token": {"agora_wrong"}, "next": {"/n/default"}})
	if resp.StatusCode != http.StatusUnauthorized || e.sessionCookie() != nil {
		t.Fatalf("bad token = %d", resp.StatusCode)
	}

	// A good token signs in and returns to where the browser was going.
	resp = e.postRaw("/login", url.Values{"token": {e.token}, "next": {"/n/default/c/c"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/n/default/c/c" {
		t.Fatalf("login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("login response is cacheable: %v", resp.Header)
	}
	set := resp.Header.Get("Set-Cookie")
	for _, want := range []string{auth.SessionCookie + "=" + core.SessionPrefix, "Path=/", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(set, want) {
			t.Fatalf("cookie %q lacks %q", set, want)
		}
	}
	if strings.Contains(set, "Secure") || strings.Contains(set, "Domain=") || strings.Contains(set, e.token) {
		t.Fatalf("cookie %q", set)
	}
	firstSession := e.sessionCookie().Value

	// Signed in: the page names the account and the comment form uses it.
	code, body = e.get("/n/default/c/c")
	if code != http.StatusOK || !strings.Contains(body, `<span class="who">pat</span>`) || strings.Contains(body, `name="name"`) {
		t.Fatalf("signed in page = %d", code)
	}
	if code, _ := e.post("/n/default/c/c/observe", url.Values{"name": {"someone"}, "body": {"hello"}}); code != http.StatusSeeOther {
		t.Fatalf("comment = %d", code)
	}
	_, body = e.get("/n/default/c/c")
	if !strings.Contains(body, "pat") || strings.Contains(body, "someone") {
		t.Fatalf("comment was not posted as pat")
	}

	// Signing in again replaces and revokes the old session.
	e.postRaw("/login", url.Values{"token": {e.token}, "next": {"/"}})
	if _, err := e.svc.AuthenticateSession(t.Context(), firstSession); core.CodeOf(err) != core.CodeUnauthorized {
		t.Fatalf("replaced session still works: %v", err)
	}
	second := e.sessionCookie().Value

	// Logging out ends the session and clears the cookie.
	resp = e.postRaw("/logout", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || e.sessionCookie() != nil {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if _, err := e.svc.AuthenticateSession(t.Context(), second); core.CodeOf(err) != core.CodeUnauthorized {
		t.Fatalf("logged out session still works: %v", err)
	}
}

func TestLoginStaysOnThisSite(t *testing.T) {
	e := newTokenEnv(t, web.Options{})
	for _, next := range []string{
		"//evil.example/", "https://evil.example/", `/\evil.example`, "relative",
		"/\t/evil.example", "/\n/evil.example", "/\r//evil.example", "/x\\..\\evil", "/\x7f/evil.example",
	} {
		resp := e.postRaw("/login", url.Values{"token": {e.token}, "next": {next}})
		if got := resp.Header.Get("Location"); got != "/" {
			t.Errorf("next %q: redirected to %q", next, got)
		}
	}
}

func TestSecureCookies(t *testing.T) {
	e := newTokenEnv(t, web.Options{SecureCookies: true})
	resp := e.postRaw("/login", url.Values{"token": {e.token}})
	if !strings.Contains(resp.Header.Get("Set-Cookie"), "Secure") {
		t.Fatalf("cookie = %q", resp.Header.Get("Set-Cookie"))
	}
}

func TestViewerIgnoresBearerTokens(t *testing.T) {
	e := newTokenEnv(t, web.Options{})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.url+"/n/default", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bearer on a page = %d", resp.StatusCode)
	}
}

func TestNoLoginPageWithAuthOff(t *testing.T) {
	e := newEnv(t, web.Options{})
	if code, _ := e.get("/login"); code != http.StatusNotFound {
		t.Fatalf("login with auth off = %d", code)
	}
}

func TestFailedLoginsAreLimited(t *testing.T) {
	e := newTokenEnv(t, web.Options{Failures: auth.NewFailures(2, nil, nil)})
	for range 2 {
		if r := e.postRaw("/login", url.Values{"token": {"agora_wrong"}}); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bad login = %d", r.StatusCode)
		}
	}
	// A further bad token is refused until the limit refills...
	if r := e.postRaw("/login", url.Values{"token": {"agora_wrong"}}); r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Fatalf("bad login after failures = %d, Retry-After %q", r.StatusCode, r.Header.Get("Retry-After"))
	}
	// ...but the right token still signs in.
	if r := e.postRaw("/login", url.Values{"token": {e.token}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("good login after failures = %d", r.StatusCode)
	}
}

func TestRateLimitedCommentsSayWhenToRetry(t *testing.T) {
	// The fixture's setup takes four of root's writes; pat has its own four.
	e := newTokenEnvWith(t, web.Options{}, core.Options{WriteLimit: 4})
	e.postRaw("/login", url.Values{"token": {e.token}})
	for i := range 4 {
		if r := e.postRaw("/n/default/c/c/observe", url.Values{"body": {"comment"}}); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("comment %d = %d", i, r.StatusCode)
		}
	}
	r := e.postRaw("/n/default/c/c/observe", url.Values{"body": {"one too many"}})
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") != "15" {
		t.Fatalf("second comment = %d, Retry-After %q", r.StatusCode, r.Header.Get("Retry-After"))
	}
}

func TestSignedInPagesAreNotStored(t *testing.T) {
	e := newTokenEnv(t, web.Options{})
	e.postRaw("/login", url.Values{"token": {e.token}})
	for _, path := range []string{"/", "/n/default", "/n/default/c/c", "/n/default/c/missing"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.url+path, nil)
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
	}
	s := openStream(t, e.env, "/n/default/c/c/events", nil)
	if got := s.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("events: Cache-Control %q", got)
	}
}
