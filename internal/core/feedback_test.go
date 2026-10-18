package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

const feedbackHint = "tell its operators with the feedback tool"

func TestFeedbackOff(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndAgreement, "a")
	st := must[core.Standing](t)(f.svc.Conclude(t.Context(), f.p, "default", "c", core.ConcludeParams{Agent: "a", Body: "AGREED: abc1234"}))
	if strings.Contains(st.Line, feedbackHint) {
		t.Fatalf("standing = %+v", st)
	}
	_, err := f.svc.SubmitFeedback(t.Context(), f.p, "default", "c", core.FeedbackParams{Agent: "a", Body: "x"})
	wantCode(t, err, core.CodeForbidden)
}

func TestFeedbackSubmitReplaces(t *testing.T) {
	f := newFixture(t, core.Options{Feedback: true})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "a", "b")
	f.say(t, "c", "a", "hello")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "b"}))

	first := must[core.FeedbackResult](t)(f.svc.SubmitFeedback(ctx, f.p, "default", "c", core.FeedbackParams{Agent: "b", Body: "first"}))
	if first.Replaced || first.Feedback.Agent != "b" || first.Feedback.Conversation != "c" || first.Feedback.Body != "first" ||
		first.Feedback.SeenSeq == 0 || first.Feedback.Account != "" {
		t.Fatalf("first = %+v", first)
	}
	f.clock.Advance(time.Minute)
	second := must[core.FeedbackResult](t)(f.svc.SubmitFeedback(ctx, f.p, "default", "c", core.FeedbackParams{Agent: "b", Body: "second"}))
	if !second.Replaced || second.Feedback.ID != first.Feedback.ID || second.Feedback.Body != "second" ||
		!second.Feedback.CreatedAt.Equal(first.Feedback.CreatedAt) || !second.Feedback.UpdatedAt.After(first.Feedback.UpdatedAt) {
		t.Fatalf("second = %+v (first %+v)", second, first)
	}
	page := must[core.FeedbackPage](t)(f.svc.ListFeedback(ctx, f.p, core.FeedbackFilter{}))
	if len(page.Feedback) != 1 || page.Feedback[0].Body != "second" {
		t.Fatalf("list = %+v", page)
	}

	for _, tc := range []struct {
		name, conv, agent, body string
		code                    core.Code
	}{
		{"never joined", "c", "zed", "x", core.CodeNotParticipant},
		{"no conversation", "nope", "b", "x", core.CodeConversationNotFound},
		{"empty", "c", "b", " \n", core.CodeInvalid},
		{"too long", "c", "b", strings.Repeat("x", core.MaxFeedbackBytes+1), core.CodeMessageTooLarge},
		{"bad agent", "c", "Bad Name", "x", core.CodeInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.SubmitFeedback(ctx, f.p, "default", tc.conv, core.FeedbackParams{Agent: tc.agent, Body: tc.body})
			wantCode(t, err, tc.code)
		})
	}
}

// The standing line invites feedback once the agent is free to stop, until
// it has left a note.
func TestFeedbackHint(t *testing.T) {
	f := newFixture(t, core.Options{Feedback: true})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "a", "b", "c")

	st := must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "a", Body: "AGREED: abc1234"}))
	if strings.Contains(st.Line, feedbackHint) {
		t.Fatalf("hint while still waiting: %s", st.Line)
	}
	// Leaving, even while expected (dropped), is free to stop too.
	st = must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "c", Body: "other work"}))
	if st.You != core.YouDropped || !strings.Contains(st.Line, feedbackHint) {
		t.Fatalf("after leave: %s", st.Line)
	}
	// The last to conclude reads the conclusion first: the hint comes with
	// done.
	st = must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "b", Body: "AGREED: abc1234"}))
	if st.Next != core.NextRead || strings.Contains(st.Line, feedbackHint) {
		t.Fatalf("after conclusion: %s", st.Line)
	}
	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "b"}))
	if r.Standing == nil || r.Standing.Next != core.NextDone || !strings.Contains(r.Standing.Line, feedbackHint) {
		t.Fatalf("after reading the conclusion: %+v", r.Standing)
	}
	must[core.FeedbackResult](t)(f.svc.SubmitFeedback(ctx, f.p, "default", "c", core.FeedbackParams{Agent: "b", Body: "fine"}))
	st = must[core.Standing](t)(f.svc.StandingOf(ctx, f.p, "default", "c", "b"))
	if st.Next != core.NextDone || strings.Contains(st.Line, feedbackHint) {
		t.Fatalf("after feedback: %s", st.Line)
	}
	// Another agent's note does not silence a's invitation.
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "a"}))
	st = must[core.Standing](t)(f.svc.StandingOf(ctx, f.p, "default", "c", "a"))
	if !strings.Contains(st.Line, feedbackHint) {
		t.Fatalf("a after b's feedback: %s", st.Line)
	}
}

func TestFeedbackAccess(t *testing.T) {
	f := newTokenFixtureWith(t, core.Options{Feedback: true})
	ctx := t.Context()
	pat, eve, val := f.account(t, "pat"), f.account(t, "eve"), f.account(t, "val")
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "pat", core.RoleMember))
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "eve", core.RoleMember))
	must[core.Membership](t)(f.svc.SetMembership(ctx, f.admin, "default", "val", core.RoleViewer))
	must[core.Conversation](t)(f.svc.Start(ctx, pat, "default", core.StartParams{Name: "c", Agent: "p", Join: true}))

	r := must[core.FeedbackResult](t)(f.svc.SubmitFeedback(ctx, pat, "default", "c", core.FeedbackParams{Agent: "p", Body: "note"}))
	if r.Feedback.Account != "pat" {
		t.Fatalf("account = %q", r.Feedback.Account)
	}
	// Another account cannot write as pat's agent, and a viewer cannot write.
	_, err := f.svc.SubmitFeedback(ctx, eve, "default", "c", core.FeedbackParams{Agent: "p", Body: "spoof"})
	wantCode(t, err, core.CodeForbidden)
	_, err = f.svc.SubmitFeedback(ctx, val, "default", "c", core.FeedbackParams{Agent: "p", Body: "spoof"})
	wantCode(t, err, core.CodeForbidden)
	// Only server admins read it.
	_, err = f.svc.ListFeedback(ctx, pat, core.FeedbackFilter{})
	wantCode(t, err, core.CodeForbidden)
	_, err = f.svc.GetFeedback(ctx, pat, r.Feedback.ID)
	wantCode(t, err, core.CodeForbidden)
	got := must[core.Feedback](t)(f.svc.GetFeedback(ctx, f.admin, r.Feedback.ID))
	if got.Body != "note" || got.Namespace != "default" {
		t.Fatalf("get = %+v", got)
	}
	_, err = f.svc.GetFeedback(ctx, f.admin, r.Feedback.ID+100)
	wantCode(t, err, core.CodeFeedbackNotFound)
}

func TestListFeedbackFilters(t *testing.T) {
	f := newFixture(t, core.Options{Feedback: true})
	ctx := t.Context()
	must[core.Namespace](t)(f.svc.CreateNamespace(ctx, f.p, "team"))
	f.start(t, "c1", core.EndAgreement, "a", "b")
	f.start(t, "c2", core.EndAgreement, "a")
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "team", core.StartParams{Name: "c1", Agent: "a", Join: true}))
	submit := func(ns, conv, agent string) {
		t.Helper()
		must[core.FeedbackResult](t)(f.svc.SubmitFeedback(ctx, f.p, ns, conv, core.FeedbackParams{Agent: agent, Body: ns + "/" + conv + "/" + agent}))
		f.clock.Advance(time.Minute)
	}
	submit("default", "c1", "a")
	submit("default", "c1", "b")
	mid := f.clock.Now()
	submit("default", "c2", "a")
	submit("team", "c1", "a")

	bodies := func(fl core.FeedbackFilter) (string, bool) {
		t.Helper()
		page := must[core.FeedbackPage](t)(f.svc.ListFeedback(ctx, f.p, fl))
		var bs []string
		for _, fb := range page.Feedback {
			bs = append(bs, fb.Body)
		}
		return strings.Join(bs, " "), page.More
	}
	for _, tc := range []struct {
		name   string
		filter core.FeedbackFilter
		want   string
		more   bool
	}{
		{"all, newest first", core.FeedbackFilter{}, "team/c1/a default/c2/a default/c1/b default/c1/a", false},
		{"namespace", core.FeedbackFilter{Namespace: "default"}, "default/c2/a default/c1/b default/c1/a", false},
		{"conversation", core.FeedbackFilter{Namespace: "default", Conversation: "c1"}, "default/c1/b default/c1/a", false},
		{"since", core.FeedbackFilter{Since: &mid}, "team/c1/a default/c2/a", false},
		{"limit", core.FeedbackFilter{Limit: 2}, "team/c1/a default/c2/a", true},
		{"none", core.FeedbackFilter{Namespace: "team", Conversation: "c9"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, more := bodies(tc.filter); got != tc.want || more != tc.more {
				t.Fatalf("got %q (more %v), want %q (more %v)", got, more, tc.want, tc.more)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		filter core.FeedbackFilter
		code   core.Code
	}{
		{"conversation without namespace", core.FeedbackFilter{Conversation: "c1"}, core.CodeInvalid},
		{"unknown namespace", core.FeedbackFilter{Namespace: "nope"}, core.CodeNamespaceNotFound},
		{"limit too high", core.FeedbackFilter{Limit: core.MaxFeedbackLimit + 1}, core.CodeInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.ListFeedback(ctx, f.p, tc.filter)
			wantCode(t, err, tc.code)
		})
	}
}
