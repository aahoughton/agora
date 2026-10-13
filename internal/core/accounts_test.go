package core_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

// tokenFixture is a token-mode service with a bootstrapped server admin.
type tokenFixture struct {
	*fixture
	admin core.Principal
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	f := newFixture(t, core.Options{Policy: core.RolePolicy()})
	tok := must[core.IssuedToken](t)(f.svc.Bootstrap(t.Context(), "root"))
	return &tokenFixture{fixture: f, admin: f.login(t, tok.Secret)}
}

func (f *fixture) login(t *testing.T, secret string) core.Principal {
	t.Helper()
	return must[core.Principal](t)(f.svc.AuthenticateToken(t.Context(), secret))
}

// account creates an account and returns a principal for a new token of it.
func (f *tokenFixture) account(t *testing.T, name string) core.Principal {
	t.Helper()
	must[core.Account](t)(f.svc.CreateAccount(t.Context(), f.admin, name, false))
	tok := must[core.IssuedToken](t)(f.svc.IssueToken(t.Context(), f.admin, name, "test", 0))
	return f.login(t, tok.Secret)
}

func TestBootstrap(t *testing.T) {
	f := newFixture(t, core.Options{Policy: core.RolePolicy()})
	ctx := t.Context()

	// Concurrent bootstraps: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won []core.IssuedToken
	for i := range 8 {
		wg.Go(func() {
			tok, err := f.svc.Bootstrap(ctx, "admin"+string(rune('a'+i)))
			if err == nil {
				mu.Lock()
				won = append(won, tok)
				mu.Unlock()
			} else if core.CodeOf(err) != core.CodeAccountExists {
				t.Errorf("bootstrap: %v", err)
			}
		})
	}
	wg.Wait()
	if len(won) != 1 || !strings.HasPrefix(won[0].Secret, core.TokenPrefix) {
		t.Fatalf("bootstraps won = %+v", won)
	}
	admin := f.login(t, won[0].Secret)
	must[core.Namespace](t)(f.svc.CreateNamespace(ctx, admin, "team"))
	id := must[core.Identity](t)(f.svc.Whoami(ctx, admin))
	if id.Account == nil || !id.Account.ServerAdmin {
		t.Fatalf("whoami = %+v", id)
	}
}

func TestRolesDecideWhatAnAccountMayDo(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	must[core.Namespace](t)(f.svc.CreateNamespace(ctx, f.admin, "team"))
	who := map[core.Role]core.Principal{}
	for _, r := range []core.Role{core.RoleViewer, core.RoleObserver, core.RoleMember, core.RoleAdmin} {
		who[r] = f.account(t, string(r)+"-pat")
		must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "team", string(r)+"-pat", r))
	}
	outsider := f.account(t, "outsider")
	must[core.Conversation](t)(f.svc.Start(ctx, who[core.RoleMember], "team", core.StartParams{Name: "c", Agent: "m"}))

	read := func(p core.Principal) error {
		_, err := f.svc.ListConversations(ctx, p, "team", core.ListParams{})
		return err
	}
	observe := func(p core.Principal) error {
		_, err := f.svc.Observe(ctx, p, "team", "c", core.ObserveParams{Name: "x", Body: "hi"})
		return err
	}
	participate := func(p core.Principal) error {
		_, err := f.svc.Join(ctx, p, "team", "c", core.JoinParams{Agent: "agent-" + string(rune('a'+p.AccountID))})
		return err
	}
	manage := func(p core.Principal) error {
		_, err := f.svc.ListMemberships(ctx, p, "team")
		return err
	}
	tests := []struct {
		name string
		p    core.Principal
		want [4]core.Code // read, observe, participate, manage
	}{
		{"viewer", who[core.RoleViewer], [4]core.Code{"", core.CodeForbidden, core.CodeForbidden, core.CodeForbidden}},
		{"observer", who[core.RoleObserver], [4]core.Code{"", "", core.CodeForbidden, core.CodeForbidden}},
		{"member", who[core.RoleMember], [4]core.Code{"", "", "", core.CodeForbidden}},
		{"admin", who[core.RoleAdmin], [4]core.Code{"", "", "", ""}},
		{"outsider", outsider, [4]core.Code{core.CodeForbidden, core.CodeForbidden, core.CodeForbidden, core.CodeForbidden}},
		{"anonymous", core.Anonymous(), [4]core.Code{core.CodeUnauthorized, core.CodeUnauthorized, core.CodeUnauthorized, core.CodeUnauthorized}},
		{"server admin", f.admin, [4]core.Code{"", "", "", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, op := range []func(core.Principal) error{read, observe, participate, manage} {
				if got := core.CodeOf(op(tt.p)); got != tt.want[i] {
					t.Errorf("op %d: code %q, want %q", i, got, tt.want[i])
				}
			}
		})
	}
	// A namespace admin manages members but cannot create namespaces.
	_, err := f.svc.CreateNamespace(ctx, who[core.RoleAdmin], "other")
	wantCode(t, err, core.CodeForbidden)
	// Namespace listings show only the namespaces an account belongs to.
	ns := must[[]core.Namespace](t)(f.svc.ListNamespaces(ctx, who[core.RoleViewer]))
	if len(ns) != 1 || ns[0].Name != "team" {
		t.Fatalf("viewer sees %+v", ns)
	}
	if ns := must[[]core.Namespace](t)(f.svc.ListNamespaces(ctx, f.admin)); len(ns) != 2 {
		t.Fatalf("server admin sees %+v", ns)
	}
}

func TestCredentialsEndAndAreRechecked(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	list := func(p core.Principal) error {
		_, err := f.svc.ListNamespaces(ctx, p)
		if err == nil {
			_, err = f.svc.Whoami(ctx, p)
		}
		return err
	}

	t.Run("revoked token", func(t *testing.T) {
		must[core.Account](t)(f.svc.CreateAccount(ctx, f.admin, "rev", false))
		tok := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "rev", "", 0))
		p := f.login(t, tok.Secret)
		if err := f.svc.RevokeToken(ctx, f.admin, tok.ID); err != nil {
			t.Fatal(err)
		}
		wantCode(t, list(p), core.CodeUnauthorized)
		_, err := f.svc.AuthenticateToken(ctx, tok.Secret)
		wantCode(t, err, core.CodeUnauthorized)
	})
	t.Run("expired token", func(t *testing.T) {
		must[core.Account](t)(f.svc.CreateAccount(ctx, f.admin, "exp", false))
		tok := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "exp", "", time.Hour))
		p := f.login(t, tok.Secret)
		f.clock.Advance(time.Hour)
		wantCode(t, list(p), core.CodeUnauthorized)
	})
	t.Run("disabled account", func(t *testing.T) {
		p := f.account(t, "dis")
		must[core.Account](t)(f.svc.UpdateAccount(ctx, f.admin, "dis", core.AccountUpdate{Disabled: ptr(true)}))
		wantCode(t, list(p), core.CodeUnauthorized)
		must[core.Account](t)(f.svc.UpdateAccount(ctx, f.admin, "dis", core.AccountUpdate{Disabled: ptr(false)}))
		if err := list(p); err != nil {
			t.Fatalf("re-enabled: %v", err)
		}
	})
	t.Run("browser session", func(t *testing.T) {
		must[core.Account](t)(f.svc.CreateAccount(ctx, f.admin, "web", false))
		tok := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "web", "", 48*time.Hour))
		p := f.login(t, tok.Secret)
		secret, _, err := f.svc.CreateSession(ctx, p, 0, "")
		if err != nil || !strings.HasPrefix(secret, core.SessionPrefix) {
			t.Fatalf("session %q, %v", secret, err)
		}
		sp := must[core.Principal](t)(f.svc.AuthenticateSession(ctx, secret))
		if sp.AccountID != p.AccountID {
			t.Fatalf("session principal = %+v", sp)
		}
		// A session cannot start another session.
		_, _, err = f.svc.CreateSession(ctx, sp, 0, "")
		wantCode(t, err, core.CodeUnauthorized)
		// It ends with its source token's expiry...
		f.clock.Advance(48 * time.Hour)
		wantCode(t, list(sp), core.CodeUnauthorized)
		// ...or its revocation, and on logout.
		tok2 := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "web", "", 0))
		s2, _, _ := f.svc.CreateSession(ctx, f.login(t, tok2.Secret), 0, "")
		s3, _, _ := f.svc.CreateSession(ctx, f.login(t, tok2.Secret), 0, "")
		if err := f.svc.RevokeToken(ctx, f.admin, tok2.ID); err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.AuthenticateSession(ctx, s2)
		wantCode(t, err, core.CodeUnauthorized)
		tok3 := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "web", "", 0))
		s4, _, _ := f.svc.CreateSession(ctx, f.login(t, tok3.Secret), 0, "")
		if err := f.svc.EndSession(ctx, s4); err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.AuthenticateSession(ctx, s4)
		wantCode(t, err, core.CodeUnauthorized)
		_ = s3

		// A login that replaces a session revokes it as it starts the new one.
		tok4 := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "web", "", 0))
		old, _, _ := f.svc.CreateSession(ctx, f.login(t, tok4.Secret), 0, "")
		if _, _, err := f.svc.CreateSession(ctx, f.login(t, tok4.Secret), 0, old); err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.AuthenticateSession(ctx, old)
		wantCode(t, err, core.CodeUnauthorized)
	})
}

func TestLastServerAdminAndRecovery(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	_, err := f.svc.UpdateAccount(ctx, f.admin, "root", core.AccountUpdate{ServerAdmin: ptr(false)})
	wantCode(t, err, core.CodeLastAdmin)
	_, err = f.svc.UpdateAccount(ctx, f.admin, "root", core.AccountUpdate{Disabled: ptr(true)})
	wantCode(t, err, core.CodeLastAdmin)

	// With a second admin, the first can step down.
	must[core.Account](t)(f.svc.CreateAccount(ctx, f.admin, "second", true))
	second := f.login(t, must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "second", "", 0)).Secret)
	must[core.Account](t)(f.svc.UpdateAccount(ctx, f.admin, "root", core.AccountUpdate{ServerAdmin: ptr(false)}))
	_, err = f.svc.CreateAccount(ctx, f.admin, "plain", false)
	wantCode(t, err, core.CodeForbidden)
	// Disabling a non-admin is never blocked, even by the last admin.
	must[core.Account](t)(f.svc.CreateAccount(ctx, second, "plain", false))
	must[core.Account](t)(f.svc.UpdateAccount(ctx, second, "plain", core.AccountUpdate{Disabled: ptr(true)}))

	// Locked out: root has no admin rights and no working token.
	if _, err := f.svc.RevokeAllTokens(ctx, f.admin, "root"); err != nil {
		t.Fatal(err)
	}
	tok := must[core.IssuedToken](t)(f.svc.Recover(ctx, "root"))
	p := f.login(t, tok.Secret)
	must[core.Namespace](t)(f.svc.CreateNamespace(ctx, p, "after-recovery"))
	_, err = f.svc.Recover(ctx, "nobody")
	wantCode(t, err, core.CodeAccountNotFound)
	_, err = f.svc.Bootstrap(ctx, "again")
	wantCode(t, err, core.CodeAccountExists)
}

func TestAccountsManageTheirOwnTokens(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	pat := f.account(t, "pat")
	f.account(t, "sam")

	mine := must[core.IssuedToken](t)(f.svc.IssueToken(ctx, pat, "pat", "laptop", 0))
	if toks := must[[]core.Token](t)(f.svc.ListTokens(ctx, pat, "pat")); len(toks) != 2 || toks[1].Label != "laptop" {
		t.Fatalf("tokens = %+v", toks)
	}
	// Another account's tokens are off limits, and so is creating accounts.
	_, err := f.svc.ListTokens(ctx, pat, "sam")
	wantCode(t, err, core.CodeForbidden)
	_, err = f.svc.IssueToken(ctx, pat, "sam", "", 0)
	wantCode(t, err, core.CodeForbidden)
	_, err = f.svc.ListTokens(ctx, pat, "nobody")
	wantCode(t, err, core.CodeForbidden)
	_, err = f.svc.CreateAccount(ctx, pat, "eve", false)
	wantCode(t, err, core.CodeForbidden)
	// Names are unique regardless of case.
	_, err = f.svc.CreateAccount(ctx, f.admin, "PAT", false)
	wantCode(t, err, core.CodeAccountExists)

	// Rotation: issue a new token, then revoke the rest.
	if n := must[int](t)(f.svc.RevokeAllTokens(ctx, pat, "pat")); n != 2 {
		t.Fatalf("revoked %d", n)
	}
	_, err = f.svc.AuthenticateToken(ctx, mine.Secret)
	wantCode(t, err, core.CodeUnauthorized)
}

func TestTokenLabelsArePrintable(t *testing.T) {
	f := newTokenFixture(t)
	pat := f.account(t, "pat")
	for _, label := range []string{"\x1b]8;;https://evil.example\x07x", "two\nlines", "\u202eevil", "\u0085", strings.Repeat("x", core.MaxLabelBytes+1)} {
		_, err := f.svc.IssueToken(t.Context(), pat, "pat", label, 0)
		wantCode(t, err, core.CodeInvalid)
	}
	must[core.IssuedToken](t)(f.svc.IssueToken(t.Context(), pat, "pat", "laptop (work) é", 0))
}
