package core_test

import (
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/core"
)

// pending runs fn in the background and returns a channel with its error.
func pending(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func await(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return")
		return nil
	}
}

// underLock holds the database's write lock, starts op (which passes its
// first authorization check and then waits for the lock), runs change in
// the held transaction, and commits. op must see the change.
func underLock(t *testing.T, f *tokenFixture, change string, args []any, op func() error) error {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil) // BEGIN IMMEDIATE: takes the write lock
	if err != nil {
		t.Fatal(err)
	}
	ch := pending(op)
	time.Sleep(100 * time.Millisecond) // op is now waiting for the lock
	if _, err := tx.ExecContext(t.Context(), change, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return await(t, ch)
}

func TestRevocationWinsAgainstAQueuedWrite(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	pat := f.account(t, "pat")
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "pat", core.RoleMember))
	must[core.Conversation](t)(f.svc.Start(ctx, pat, "default", core.StartParams{Name: "c", Agent: "p", Join: true}))

	t.Run("issuing a token after its account's tokens are revoked", func(t *testing.T) {
		err := underLock(t, f, `UPDATE tokens SET revoked_at = 1 WHERE account_id = ?`, []any{pat.AccountID}, func() error {
			_, err := f.svc.IssueToken(ctx, pat, "pat", "late", 0)
			return err
		})
		wantCode(t, err, core.CodeUnauthorized)
	})
	t.Run("posting after the membership is removed", func(t *testing.T) {
		sam := f.account(t, "sam")
		must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "sam", core.RoleMember))
		must[core.Standing](t)(f.svc.Join(ctx, sam, "default", "c", core.JoinParams{Agent: "s"}))
		err := underLock(t, f, `DELETE FROM memberships WHERE account_id = ?`, []any{sam.AccountID}, func() error {
			_, err := f.svc.Say(ctx, sam, "default", "c", core.SayParams{Agent: "s", Body: "too late"})
			return err
		})
		wantCode(t, err, core.CodeForbidden)
	})
	t.Run("an admin change after demotion", func(t *testing.T) {
		boss := f.account(t, "boss")
		must[core.Account](t)(f.svc.UpdateAccount(ctx, f.admin, "boss", core.AccountUpdate{ServerAdmin: ptr(true)}))
		err := underLock(t, f, `UPDATE accounts SET server_admin = 0 WHERE id = ?`, []any{boss.AccountID}, func() error {
			_, err := f.svc.CreateAccount(ctx, boss, "eve", true)
			return err
		})
		wantCode(t, err, core.CodeForbidden)
	})
}
