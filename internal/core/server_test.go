package core_test

import (
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/core"
)

func TestServerActivity(t *testing.T) {
	f := newTokenFixture(t)
	f.p = f.admin
	ctx := t.Context()

	empty := must[core.ServerActivity](t)(f.svc.ServerActivity(ctx, f.admin, f.clock.Now().Add(-time.Hour)))
	if empty.LastMessage != nil || len(empty.Conversations) != 0 {
		t.Fatalf("empty server: %+v", empty)
	}

	f.start(t, "old", core.EndAgreement, "a")
	f.clock.Advance(48 * time.Hour)
	f.start(t, "busy", core.EndAgreement, "a", "b")
	f.say(t, "busy", "a", "hello")
	f.read(t, "busy", "b")
	since := f.clock.Now().Add(-24 * time.Hour)

	// A viewer keeps "old" listed although it is quiet; a waiting agent
	// is counted on "busy".
	v := must[*core.Viewer](t)(f.svc.OpenViewer(ctx, f.admin, "default", "old"))
	defer v.Close()
	waited := pending(func() error {
		_, err := f.svc.Wait(ctx, f.admin, "default", "busy", core.WaitParams{Agent: "b", Timeout: 10 * time.Second})
		return err
	})
	var got core.ServerActivity
	for deadline := time.Now().Add(5 * time.Second); ; {
		got = must[core.ServerActivity](t)(f.svc.ServerActivity(ctx, f.admin, since))
		if len(got.Conversations) > 0 && got.Conversations[0].Waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the waiter was not counted: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lm := got.LastMessage; lm == nil || lm.Conversation != "busy" || lm.Author != "a" || lm.Kind != core.KindSay || !lm.At.Equal(f.clock.Now()) {
		t.Fatalf("last message = %+v", got.LastMessage)
	}
	if len(got.Conversations) != 2 {
		t.Fatalf("conversations = %+v", got.Conversations)
	}
	if busy := got.Conversations[0]; busy.Name != "busy" || busy.Active != 2 || busy.Viewing != 0 {
		t.Fatalf("busy = %+v", busy)
	}
	if old := got.Conversations[1]; old.Name != "old" || old.Active != 1 || old.Waiting != 0 || old.Viewing != 1 {
		t.Fatalf("old = %+v", old)
	}

	f.say(t, "busy", "a", "wake up")
	if err := await(t, waited); err != nil {
		t.Fatal(err)
	}

	member := f.account(t, "pat")
	_, err := f.svc.ServerActivity(ctx, member, since)
	wantCode(t, err, core.CodeForbidden)
}
