package core_test

import (
	"slices"
	"testing"

	"github.com/aahoughton/agora/internal/core"
)

// A post records how far its author had read, and Unseen names the
// messages from others it was written without.
func TestSeenSeq(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{Name: "c", Agent: "alice", Join: true}))
	must[core.Standing](t)(f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "bob"}))
	read := func(agent string) {
		t.Helper()
		must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: agent}))
	}
	say := func(agent, body string) {
		t.Helper()
		must[core.SayResult](t)(f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: agent, Body: body}))
	}

	read("alice")
	say("bob", "b1")   // 4
	say("alice", "a1") // 5: written without reading b1
	say("alice", "a2") // 6: still unread; a1 is alice's own
	read("alice")
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "done"})) // 7
	must[core.Message](t)(f.svc.Observe(ctx, f.p, "default", "c", core.ObserveParams{Name: "pat", Body: "p1"}))         // 8
	read("bob")
	say("alice", "a3") // 10, after the system line withdrawing the conclusion
	// bob's "bye" is 11 and the line saying he left 12.
	must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "bob", Body: "bye"}))
	must[core.Standing](t)(f.svc.Close(ctx, f.p, "default", "c", core.CloseParams{Agent: "alice", Body: "closing"}))

	zero := int64(0)
	all := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{After: &zero, Limit: 100})).Messages
	type row struct {
		seen   int64 // -1 for none
		unseen []int64
	}
	got := map[string]row{}
	for _, m := range all {
		r := row{seen: -1, unseen: core.Unseen(m, all)}
		if m.SeenSeq != nil {
			r.seen = *m.SeenSeq
		}
		got[m.Kind+" "+m.Body] = r
	}
	for key, want := range map[string]row{
		"say b1":           {seen: 0},
		"say a1":           {seen: 3, unseen: []int64{4}},
		"say a2":           {seen: 3, unseen: []int64{4}},
		"conclude done":    {seen: 6},
		"observer p1":      {seen: -1},
		"say a3":           {seen: 6, unseen: []int64{8}},
		"say bye":          {seen: 8, unseen: []int64{10}},
		"conclude closing": {seen: 6, unseen: []int64{8, 11}},
	} {
		g, ok := got[key]
		if !ok {
			t.Errorf("no message %q", key)
			continue
		}
		if g.seen != want.seen || !slices.Equal(g.unseen, want.unseen) {
			t.Errorf("%s: seen %d unseen %v, want seen %d unseen %v", key, g.seen, g.unseen, want.seen, want.unseen)
		}
	}
	for _, m := range all {
		if m.Kind == core.KindSystem && m.SeenSeq != nil {
			t.Errorf("system line #%d has seen_seq %d", m.Seq, *m.SeenSeq)
		}
	}
}
