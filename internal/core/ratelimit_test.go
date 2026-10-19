package core_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/core"
)

func TestWritesAreLimitedPerAccount(t *testing.T) {
	f := newTokenFixtureWith(t, core.Options{WriteLimit: 4})
	ctx := t.Context()
	pat := f.account(t, "pat")
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "pat", core.RoleMember))

	must[core.Conversation](t)(f.svc.Start(ctx, pat, "default", core.StartParams{Name: "c", Agent: "p", Join: true}))
	for range 3 {
		must[core.SayResult](t)(f.svc.Say(ctx, pat, "default", "c", core.SayParams{Agent: "p", Body: "hi"}))
	}
	_, err := f.svc.Say(ctx, pat, "default", "c", core.SayParams{Agent: "p", Body: "one too many"})
	ce, ok := errors.AsType[*core.Error](err)
	if !ok || ce.Code != core.CodeRateLimited || ce.RetryAfter != 15*time.Second {
		t.Fatalf("fifth write: %v", err)
	}
	// Reads and waits are not writes.
	for range 10 {
		must[core.ReadResult](t)(f.svc.Read(ctx, pat, "default", "c", core.ReadParams{Agent: "p"}))
		must[[]core.Standing](t)(f.svc.Status(ctx, pat, "default", "p"))
	}
	// Another account has its own allowance, and it refills over time.
	must[core.Standing](t)(f.svc.Join(ctx, f.admin, "default", "c", core.JoinParams{Agent: "root-agent"}))
	f.clock.Advance(15 * time.Second)
	must[core.SayResult](t)(f.svc.Say(ctx, pat, "default", "c", core.SayParams{Agent: "p", Body: "later"}))
}

func TestAuthOffIsNeverLimited(t *testing.T) {
	f := newFixture(t, core.Options{WriteLimit: 1})
	ctx := t.Context()
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{Name: "c", Agent: "a", Join: true}))
	for range 5 {
		must[core.SayResult](t)(f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: "a", Body: "hi"}))
	}
}
