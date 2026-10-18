package core_test

import (
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

// TestAgentNamesBelongToAccounts checks that, in token mode, every operation
// that acts as an agent refuses an account other than the one that joined
// with that name.
func TestAgentNamesBelongToAccounts(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	pat, sam := f.account(t, "pat"), f.account(t, "sam")
	for _, a := range []string{"pat", "sam"} {
		must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", a, core.RoleMember))
	}
	must[core.Conversation](t)(f.svc.Start(ctx, pat, "default", core.StartParams{
		Name: "c", Agent: "pat-lead", End: core.EndCoordinator, Join: true,
	}))
	must[core.Standing](t)(f.svc.Join(ctx, pat, "default", "c", core.JoinParams{Agent: "pat-helper"}))
	must[core.Standing](t)(f.svc.Join(ctx, sam, "default", "c", core.JoinParams{Agent: "sam-agent"}))

	ops := map[string]func(p core.Principal, agent string) error{
		"join resume": func(p core.Principal, agent string) error {
			_, err := f.svc.Join(ctx, p, "default", "c", core.JoinParams{Agent: agent, Resume: true})
			return err
		},
		"say": func(p core.Principal, agent string) error {
			_, err := f.svc.Say(ctx, p, "default", "c", core.SayParams{Agent: agent, Body: "hi"})
			return err
		},
		"read": func(p core.Principal, agent string) error {
			_, err := f.svc.Read(ctx, p, "default", "c", core.ReadParams{Agent: agent})
			return err
		},
		"wait": func(p core.Principal, agent string) error {
			_, err := f.svc.Wait(ctx, p, "default", "c", core.WaitParams{Agent: agent, Timeout: time.Millisecond})
			return err
		},
		"standing": func(p core.Principal, agent string) error {
			_, err := f.svc.StandingOf(ctx, p, "default", "c", agent)
			return err
		},
		"presence": func(p core.Principal, agent string) error {
			_, err := f.svc.SetPresence(ctx, p, "default", "c", core.PresenceParams{Agent: agent, Presence: core.PresenceAway})
			return err
		},
		"conclude": func(p core.Principal, agent string) error {
			_, err := f.svc.Conclude(ctx, p, "default", "c", core.ConcludeParams{Agent: agent, Body: "done"})
			return err
		},
		"leave": func(p core.Principal, agent string) error {
			_, err := f.svc.Leave(ctx, p, "default", "c", core.LeaveParams{Agent: agent})
			return err
		},
		"close": func(p core.Principal, agent string) error {
			_, err := f.svc.Close(ctx, p, "default", "c", core.CloseParams{Agent: agent})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			wantCode(t, op(sam, "pat-helper"), core.CodeForbidden)
		})
	}
	// Close as the creator's name is refused to another account too.
	wantCode(t, ops["close"](sam, "pat-lead"), core.CodeForbidden)
	// And a left name stays bound: sam cannot take pat's after pat leaves.
	must[core.Standing](t)(f.svc.Leave(ctx, pat, "default", "c", core.LeaveParams{Agent: "pat-helper"}))
	_, err := f.svc.Join(ctx, sam, "default", "c", core.JoinParams{Agent: "pat-helper"})
	wantCode(t, err, core.CodeForbidden)
	// The owner can come back, and a never-used name is free to claim.
	must[core.Standing](t)(f.svc.Join(ctx, pat, "default", "c", core.JoinParams{Agent: "pat-helper"}))
	must[core.Standing](t)(f.svc.Join(ctx, sam, "default", "c", core.JoinParams{Agent: "sam-second"}))

	// Status lists only the account's own participations.
	if sts := must[[]core.Standing](t)(f.svc.Status(ctx, sam, "default", "pat-helper")); len(sts) != 0 {
		t.Fatalf("sam sees pat's standing: %+v", sts)
	}
	if sts := must[[]core.Standing](t)(f.svc.Status(ctx, pat, "default", "pat-lead")); len(sts) != 1 {
		t.Fatalf("pat's coordinator standing = %+v", sts)
	}

	// The owner's own calls all work, ending with the coordinator closing.
	for _, name := range []string{"say", "read", "wait", "standing", "presence", "conclude", "join resume", "leave"} {
		if err := ops[name](pat, "pat-helper"); err != nil {
			t.Fatalf("%s as owner: %v", name, err)
		}
	}
	if err := ops["close"](pat, "pat-lead"); err != nil {
		t.Fatalf("close as owner: %v", err)
	}
}

func TestReadingAsAnAgentNeedsParticipate(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	member, observer := f.account(t, "member"), f.account(t, "observer")
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "member", core.RoleMember))
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "observer", core.RoleObserver))
	must[core.Conversation](t)(f.svc.Start(ctx, member, "default", core.StartParams{Name: "c", Agent: "m", Join: true}))

	must[core.ReadResult](t)(f.svc.Read(ctx, observer, "default", "c", core.ReadParams{}))
	_, err := f.svc.Read(ctx, observer, "default", "c", core.ReadParams{Agent: "m"})
	wantCode(t, err, core.CodeForbidden)
}

func TestAccountsCommentUnderTheirOwnName(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	pat := f.account(t, "pat")
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "pat", core.RoleObserver))
	must[core.Conversation](t)(f.svc.Start(ctx, f.admin, "default", core.StartParams{Name: "c", Agent: "a"}))
	m := must[core.Message](t)(f.svc.Observe(ctx, pat, "default", "c", core.ObserveParams{Name: "someone-else", Body: "hi"}))
	if m.Author != "pat" {
		t.Fatalf("observer comment author = %q", m.Author)
	}
}

func TestNamesJoinedWithoutAnAccountStayUnclaimed(t *testing.T) {
	// One database, used first with auth off, then with tokens.
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	must[core.Conversation](t)(f.svc.Start(ctx, core.Anonymous(), "default", core.StartParams{Name: "c", Agent: "old", Join: true}))

	tf := &tokenFixture{fixture: &fixture{svc: core.New(f.db, core.Options{Policy: core.RolePolicy(), Now: f.clock.Now}), clock: f.clock}}
	tok := must[core.IssuedToken](t)(tf.svc.Bootstrap(ctx, "root"))
	tf.admin = tf.login(t, tok.Secret)
	_, err := tf.svc.Say(ctx, tf.admin, "default", "c", core.SayParams{Agent: "old", Body: "mine now"})
	wantCode(t, err, core.CodeForbidden)
	_, err = tf.svc.Join(ctx, tf.admin, "default", "c", core.JoinParams{Agent: "old", Resume: true})
	wantCode(t, err, core.CodeForbidden)
	must[core.Standing](t)(tf.svc.Join(ctx, tf.admin, "default", "c", core.JoinParams{Agent: "new"}))
}

func TestMessagesRecordTheActingAccount(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	must[core.Conversation](t)(f.svc.Start(ctx, f.admin, "default", core.StartParams{Name: "c", Agent: "a", Join: true}))
	m := must[core.SayResult](t)(f.svc.Say(ctx, f.admin, "default", "c", core.SayParams{Agent: "a", Body: "hi"}))
	var account, owner, creator int64
	err := f.db.QueryRowContext(ctx, `SELECT m.account_id, p.account_id, c.created_by_account
		FROM messages m, participants p, conversations c WHERE m.seq = ? AND p.agent = 'a'`, m.Message.Seq).Scan(&account, &owner, &creator)
	if err != nil || account != f.admin.AccountID || owner != f.admin.AccountID || creator != f.admin.AccountID {
		t.Fatalf("recorded accounts %d %d %d (want %d), %v", account, owner, creator, f.admin.AccountID, err)
	}
}

// TestYourAccount checks that messages and participants say, for each
// caller, whether the caller's account posted or joined them.
func TestYourAccount(t *testing.T) {
	f := newTokenFixture(t)
	ctx := t.Context()
	pat, sam := f.account(t, "pat"), f.account(t, "sam")
	for _, a := range []string{"pat", "sam"} {
		must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", a, core.RoleMember))
	}
	// A second token of pat's account is the same account.
	patToo := f.login(t, must[core.IssuedToken](t)(f.svc.IssueToken(ctx, f.admin, "pat", "other", 0)).Secret)

	must[core.Conversation](t)(f.svc.Start(ctx, pat, "default", core.StartParams{Name: "c", Agent: "pat-agent", End: core.EndAgreement, Join: true}))
	must[core.Standing](t)(f.svc.Join(ctx, sam, "default", "c", core.JoinParams{Agent: "sam-agent"}))
	must[core.ReadResult](t)(f.svc.Read(ctx, pat, "default", "c", core.ReadParams{Agent: "pat-agent"}))
	must[core.ReadResult](t)(f.svc.Read(ctx, sam, "default", "c", core.ReadParams{Agent: "sam-agent"}))
	must[core.Message](t)(f.svc.Observe(ctx, patToo, "default", "c", core.ObserveParams{Body: "from pat"}))
	must[core.Message](t)(f.svc.Observe(ctx, sam, "default", "c", core.ObserveParams{Body: "from sam"}))

	yours := func(ms []core.Message) map[string]bool {
		out := map[string]bool{}
		for _, m := range ms {
			if m.Kind == core.KindObserver {
				out[m.Body] = m.YourAccount
			}
		}
		return out
	}
	check := func(where string, got map[string]bool, patMine bool) {
		t.Helper()
		if got["from pat"] != patMine || got["from sam"] == patMine {
			t.Fatalf("%s: your_account = %v", where, got)
		}
	}

	d := must[core.Digest](t)(f.svc.Wait(ctx, pat, "default", "c", core.WaitParams{Agent: "pat-agent", Timeout: time.Millisecond}))
	previews := map[string]bool{}
	for _, pv := range d.Previews {
		previews[pv.Text] = pv.YourAccount
	}
	check("pat's wait", previews, true)
	r := must[core.ReadResult](t)(f.svc.Read(ctx, sam, "default", "c", core.ReadParams{Agent: "sam-agent"}))
	check("sam's read", yours(r.Messages), false)

	// The comments sam had not read come back with sam's post.
	must[core.Message](t)(f.svc.Observe(ctx, pat, "default", "c", core.ObserveParams{Body: "again from pat"}))
	sr := must[core.SayResult](t)(f.svc.Say(ctx, sam, "default", "c", core.SayParams{Agent: "sam-agent", Body: "noted", InlineBytes: 4096}))
	if !sr.Message.YourAccount {
		t.Fatal("sam's own post is not sam's account")
	}
	if sr.Missed == nil || len(sr.Missed.Messages) != 1 || sr.Missed.Messages[0].YourAccount {
		t.Fatalf("missed = %+v", sr.Missed)
	}

	pt := must[core.Participant](t)(f.svc.SetPresence(ctx, sam, "default", "c", core.PresenceParams{Agent: "sam-agent", Presence: core.PresenceAway}))
	if !pt.YourAccount {
		t.Fatal("sam's presence result is not sam's account")
	}

	// Concluding records a system message for the request's account; it
	// is still the server's, not the caller's.
	must[core.Standing](t)(f.svc.Conclude(ctx, pat, "default", "c", core.ConcludeParams{Agent: "pat-agent", Body: "AGREED: abc1234"}))
	must[core.Standing](t)(f.svc.Conclude(ctx, sam, "default", "c", core.ConcludeParams{Agent: "sam-agent", Body: "AGREED: abc1234"}))
	all := must[core.ReadResult](t)(f.svc.Read(ctx, sam, "default", "c", core.ReadParams{}))
	for _, m := range all.Messages {
		if m.Kind == core.KindSystem && m.YourAccount {
			t.Fatalf("system message %+v is marked as sam's", m)
		}
	}

	plain := must[core.ReadResult](t)(f.svc.Read(ctx, f.admin, "default", "c", core.ReadParams{}))
	for _, m := range plain.Messages {
		if m.YourAccount {
			t.Fatalf("the admin's read marks %+v as the admin's", m)
		}
	}

	for _, tc := range []struct {
		who  core.Principal
		mine string
	}{{pat, "pat-agent"}, {patToo, "pat-agent"}, {sam, "sam-agent"}, {f.admin, ""}} {
		c := must[core.Conversation](t)(f.svc.GetConversation(ctx, tc.who, "default", "c"))
		page := must[core.ListPage](t)(f.svc.ListConversations(ctx, tc.who, "default", core.ListParams{}))
		for _, ps := range [][]core.Participant{c.Participants, page.Conversations[0].Participants} {
			for _, p := range ps {
				if p.YourAccount != (p.Agent == tc.mine) {
					t.Fatalf("participant %s: your_account = %v for the caller owning %q", p.Agent, p.YourAccount, tc.mine)
				}
			}
		}
	}
}

// TestYourAccountWithoutAuth checks that nothing is anyone's account when
// auth is off.
func TestYourAccountWithoutAuth(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice")
	must[core.Message](t)(f.svc.Observe(ctx, f.p, "default", "c", core.ObserveParams{Name: "pat", Body: "hi"}))
	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice"}))
	for _, m := range r.Messages {
		if m.YourAccount {
			t.Fatalf("message %+v is marked as the caller's", m)
		}
	}
	if f.get(t, "c").Participants[0].YourAccount {
		t.Fatal("alice is marked as the caller's")
	}
}
