package core_test

import (
	"context"
	"errors"
	"sync"
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
	// An agent's reads and waits have their own allowance, so one out of
	// writes can still catch up. Reads without an agent are not limited.
	for range 2 {
		must[core.ReadResult](t)(f.svc.Read(ctx, pat, "default", "c", core.ReadParams{Agent: "p"}))
		must[core.Digest](t)(f.svc.Wait(ctx, pat, "default", "c", core.WaitParams{Agent: "p"}))
	}
	_, err = f.svc.Wait(ctx, pat, "default", "c", core.WaitParams{Agent: "p"})
	if ce, ok := errors.AsType[*core.Error](err); !ok || ce.Code != core.CodeRateLimited {
		t.Fatalf("fifth wait: %v", err)
	}
	_, err = f.svc.Read(ctx, pat, "default", "c", core.ReadParams{Agent: "p"})
	if ce, ok := errors.AsType[*core.Error](err); !ok || ce.Code != core.CodeRateLimited {
		t.Fatalf("fifth agent read: %v", err)
	}
	for range 10 {
		must[core.ReadResult](t)(f.svc.Read(ctx, pat, "default", "c", core.ReadParams{}))
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

func TestOneAccountCannotTakeEverySlot(t *testing.T) {
	f := newTokenFixtureWith(t, core.Options{MaxWaiters: 3, MaxWatchers: 3, MaxPerAccount: 2})
	ctx := t.Context()
	pat := f.account(t, "pat")
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "pat", core.RoleMember))
	must[core.Conversation](t)(f.svc.Start(ctx, f.admin, "default", core.StartParams{Name: "c", Agent: "boss", Join: true}))
	for _, a := range []string{"p1", "p2", "p3"} {
		must[core.Standing](t)(f.svc.Join(ctx, pat, "default", "c", core.JoinParams{Agent: a}))
	}
	last := must[core.Conversation](t)(f.svc.GetConversation(ctx, f.admin, "default", "c")).LastSeq

	blockCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, a := range []string{"p1", "p2"} {
		wg.Go(func() {
			_, _ = f.svc.Wait(blockCtx, pat, "default", "c", core.WaitParams{Agent: a, After: new(last), Timeout: time.Minute})
		})
		wg.Go(func() { _, _ = f.svc.Follow(blockCtx, pat, "default", "c", last, time.Minute) })
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		act := must[core.ServerActivity](t)(f.svc.ServerActivity(ctx, f.admin, time.Time{}))
		if len(act.Conversations) == 1 && act.Conversations[0].Waiting == 2 && act.Conversations[0].Viewing == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waits and follows never started: %+v", act.Conversations)
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, err := f.svc.Wait(ctx, pat, "default", "c", core.WaitParams{Agent: "p3", After: new(last), Timeout: time.Minute})
	wantCode(t, err, core.CodeTooManyWaiters)
	_, err = f.svc.Follow(ctx, pat, "default", "c", last, time.Minute)
	wantCode(t, err, core.CodeTooManyWatchers)
	// Another account still has room.
	must[core.Digest](t)(f.svc.Wait(ctx, f.admin, "default", "c", core.WaitParams{Agent: "boss", After: new(last)}))
	must[core.ReadResult](t)(f.svc.Follow(ctx, f.admin, "default", "c", last, 0))

	// Places are given back when waits and follows end.
	cancel()
	wg.Wait()
	must[core.Digest](t)(f.svc.Wait(ctx, pat, "default", "c", core.WaitParams{Agent: "p3", After: new(last)}))
	must[core.ReadResult](t)(f.svc.Follow(ctx, pat, "default", "c", last, 0))
}
