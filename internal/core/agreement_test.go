package core_test

import (
	"testing"

	"github.com/aahoughton/agora/internal/core"
)

func TestAgreementComparesConclusions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bodies []string // one per participant, in order
		// want is the last system line; a conversation that concludes
		// starts it with "conversation concluded".
		want string
	}{
		{
			"same message",
			[]string{"AGREED: #3", "AGREED: #3, the final text"},
			"conversation concluded: every remaining participant agreed to #3",
		},
		{
			"short and full sha",
			[]string{"AGREED: ABC1234.", "AGREED: abc1234def0\n\nnotes"},
			"conversation concluded: every remaining participant agreed to abc1234def0",
		},
		{
			"different messages",
			[]string{"AGREED: #3", "AGREED: #4"},
			"conclusions differ: a agreed to #3, b agreed to #4; all of them are active again",
		},
		{
			"shas that only share a prefix",
			[]string{"AGREED: abc1234", "AGREED: abc1234aa", "AGREED: abc1234bb"},
			"conclusions differ: a agreed to abc1234, b agreed to abc1234aa, c agreed to abc1234bb; all of them are active again",
		},
		{
			"every one blocked",
			[]string{"BLOCKED: the tests fail", "BLOCKED: same"},
			"conversation concluded: every remaining participant blocked",
		},
		{
			"quoted and punctuated refs",
			[]string{"\n  AGREED:\t`#3`!", "AGREED: (#3) after all", "AGREED: #3\tok"},
			"conversation concluded: every remaining participant agreed to #3",
		},
		{
			"a block",
			[]string{"AGREED: #3", "BLOCKED: the tests fail"},
			"conclusions differ: a agreed to #3, b blocked; all of them are active again",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, core.Options{})
			agents := []string{"a", "b", "c"}[:len(tc.bodies)]
			f.start(t, "c", core.EndAgreement, agents...)
			var st core.Standing
			for i, body := range tc.bodies {
				st = must[core.Standing](t)(f.svc.Conclude(t.Context(), f.p, "default", "c", core.ConcludeParams{Agent: agents[i], Body: body}))
			}
			msgs := f.read(t, "c", agents[0]).Messages
			if got := msgs[len(msgs)-1]; got.Kind != core.KindSystem || got.Body != tc.want {
				t.Fatalf("last line %s %q, want %q", got.Kind, got.Body, tc.want)
			}
			c := f.get(t, "c")
			if c.Status == core.StatusConcluded {
				return
			}
			// Conclusions that differ leave everyone active and expected.
			if !st.Expected || st.You != core.YouActive {
				t.Fatalf("last to conclude: %+v", st)
			}
			for _, a := range agents {
				if p := participant(t, c, a); p.State != core.StateActive {
					t.Fatalf("%s is %s", a, p.State)
				}
			}
		})
	}
}

func TestAgreementNeedsAPosition(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndAgreement, "a")
	for _, body := range []string{"done", "AGREED", "AGREED:", "AGREED: LRU", "AGREED: abc123", "AGREED: #0", "BLOCKED:", "agreed: #1"} {
		_, err := f.svc.Conclude(t.Context(), f.p, "default", "c", core.ConcludeParams{Agent: "a", Body: body})
		if core.CodeOf(err) != core.CodeInvalid {
			t.Errorf("%q: %v", body, err)
		}
	}
	// Other end conditions take any conclusion.
	f.start(t, "free", core.EndNone, "a")
	must[core.Standing](t)(f.svc.Conclude(t.Context(), f.p, "default", "free", core.ConcludeParams{Agent: "a", Body: "done"}))
}
