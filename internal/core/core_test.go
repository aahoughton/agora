package core_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	svc   *core.Service
	clock *clock
	p     core.Principal
}

func newFixture(t *testing.T, opts core.Options) *fixture {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	clk := &clock{t: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	opts.Now = clk.Now
	return &fixture{svc: core.New(db, opts), clock: clk, p: core.Anonymous()}
}

func ptr[T any](v T) *T { return &v }

func wantCode(t *testing.T, err error, code core.Code) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("err = %v, want code %q", err, code)
	}
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// start creates conversation name in default with creator a, joined.
func (f *fixture) start(t *testing.T, name string, end core.EndCondition, agents ...string) core.Conversation {
	t.Helper()
	c := must[core.Conversation](t)(f.svc.Start(t.Context(), f.p, "default", core.StartParams{
		Name: name, End: end, Agent: agents[0], Join: true,
	}))
	for _, a := range agents[1:] {
		must[core.Standing](t)(f.svc.Join(t.Context(), f.p, "default", name, core.JoinParams{Agent: a}))
	}
	return c
}

func (f *fixture) say(t *testing.T, conv, agent, body string) core.SayResult {
	t.Helper()
	return must[core.SayResult](t)(f.svc.Say(t.Context(), f.p, "default", conv, core.SayParams{Agent: agent, Body: body}))
}

func (f *fixture) get(t *testing.T, conv string) core.Conversation {
	t.Helper()
	return must[core.Conversation](t)(f.svc.GetConversation(t.Context(), f.p, "default", conv))
}

func participant(t *testing.T, c core.Conversation, agent string) core.Participant {
	t.Helper()
	for _, p := range c.Participants {
		if p.Agent == agent {
			return p
		}
	}
	t.Fatalf("no participant %q in %+v", agent, c.Participants)
	return core.Participant{}
}

func TestNamespaces(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()

	ns := must[[]core.Namespace](t)(f.svc.ListNamespaces(ctx, f.p))
	if len(ns) != 1 || ns[0].Name != "default" {
		t.Fatalf("namespaces = %+v", ns)
	}

	must[core.Namespace](t)(f.svc.CreateNamespace(ctx, f.p, "team-blue"))
	_, err := f.svc.CreateNamespace(ctx, f.p, "team-blue")
	wantCode(t, err, core.CodeNamespaceExists)
	_, err = f.svc.CreateNamespace(ctx, f.p, "../etc")
	wantCode(t, err, core.CodeInvalid)

	_, err = f.svc.ListConversations(ctx, f.p, "nope", core.ListParams{})
	wantCode(t, err, core.CodeNamespaceNotFound)
}

func TestStartValidation(t *testing.T) {
	f := newFixture(t, core.Options{})
	future := f.clock.Now().Add(time.Hour)
	past := f.clock.Now().Add(-time.Hour)
	tests := []struct {
		name string
		sp   core.StartParams
		code core.Code
	}{
		{"bad name", core.StartParams{Name: "-x", Agent: "a"}, core.CodeInvalid},
		{"bad agent", core.StartParams{Name: "x", Agent: ""}, core.CodeInvalid},
		{"bad end", core.StartParams{Name: "x", Agent: "a", End: "vote"}, core.CodeInvalid},
		{"timebox no deadline", core.StartParams{Name: "x", Agent: "a", End: core.EndTimebox}, core.CodeInvalid},
		{"timebox past", core.StartParams{Name: "x", Agent: "a", End: core.EndTimebox, EndsAt: &past}, core.CodeInvalid},
		{"deadline without timebox", core.StartParams{Name: "x", Agent: "a", End: core.EndAgreement, EndsAt: &future}, core.CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Start(t.Context(), f.p, "default", tt.sp)
			wantCode(t, err, tt.code)
		})
	}

	_, err := f.svc.Start(t.Context(), f.p, "missing", core.StartParams{Name: "x", Agent: "a"})
	wantCode(t, err, core.CodeNamespaceNotFound)
}

func TestStartRecordsCreatorAndLog(t *testing.T) {
	f := newFixture(t, core.Options{})
	c := f.start(t, "design", core.EndAgreement, "alice")
	if c.Status != core.StatusOpen || c.CreatedBy != "alice" || c.End != core.EndAgreement {
		t.Fatalf("conversation = %+v", c)
	}
	if len(c.Participants) != 1 || c.Participants[0].Agent != "alice" || c.Participants[0].State != core.StateActive {
		t.Fatalf("participants = %+v", c.Participants)
	}
	r := must[core.ReadResult](t)(f.svc.Read(t.Context(), f.p, "default", "design", core.ReadParams{}))
	if len(r.Messages) != 2 || r.Messages[0].Kind != core.KindSystem || r.Messages[1].Body != "alice joined" {
		t.Fatalf("log = %+v", r.Messages)
	}

	_, err := f.svc.Start(t.Context(), f.p, "default", core.StartParams{Name: "design", Agent: "bob"})
	wantCode(t, err, core.CodeConversationExists)

	// Without Join the creator is not a participant.
	c2 := must[core.Conversation](t)(f.svc.Start(t.Context(), f.p, "default", core.StartParams{Name: "solo", Agent: "coord"}))
	if len(c2.Participants) != 0 {
		t.Fatalf("participants = %+v", c2.Participants)
	}
}

func TestJoinResumeAndRejoin(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")

	_, err := f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "bob"})
	wantCode(t, err, core.CodeNameActive)

	st := must[core.Standing](t)(f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "bob", Resume: true}))
	if st.You != core.YouActive {
		t.Fatalf("standing = %+v", st)
	}

	must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "bob"}))
	if got := participant(t, f.get(t, "c"), "bob").State; got != core.StateLeft {
		t.Fatalf("state = %q", got)
	}
	must[core.Standing](t)(f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "bob"}))
	if got := participant(t, f.get(t, "c"), "bob").State; got != core.StateActive {
		t.Fatalf("state = %q", got)
	}

	_, err = f.svc.Join(ctx, f.p, "default", "missing", core.JoinParams{Agent: "bob"})
	wantCode(t, err, core.CodeConversationNotFound)
}

func TestSay(t *testing.T) {
	f := newFixture(t, core.Options{MaxBodyBytes: 16})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	last := f.get(t, "c").LastSeq

	res := f.say(t, "c", "alice", "hello")
	if res.Message.Seq != last+1 || res.Message.Kind != core.KindSay || res.Message.Author != "alice" {
		t.Fatalf("message = %+v", res.Message)
	}

	tests := []struct {
		name string
		sp   core.SayParams
		code core.Code
	}{
		{"empty", core.SayParams{Agent: "alice", Body: "  "}, core.CodeInvalid},
		{"too large", core.SayParams{Agent: "alice", Body: strings.Repeat("x", 17)}, core.CodeMessageTooLarge},
		{"stranger", core.SayParams{Agent: "carol", Body: "hi"}, core.CodeNotParticipant},
		{"stale", core.SayParams{Agent: "bob", Body: "hi", ExpectedLastSeq: ptr(last)}, core.CodeStaleContext},
		{"reply to future", core.SayParams{Agent: "bob", Body: "hi", ReplyTo: ptr(last + 5)}, core.CodeInvalid},
		{"reply to zero", core.SayParams{Agent: "bob", Body: "hi", ReplyTo: ptr(int64(0))}, core.CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Say(ctx, f.p, "default", "c", tt.sp)
			wantCode(t, err, tt.code)
		})
	}

	_, err := f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: "bob", Body: "hi", ExpectedLastSeq: ptr(last)})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.LastSeq != last+1 {
		t.Fatalf("stale error = %#v", err)
	}

	ok := must[core.SayResult](t)(f.svc.Say(ctx, f.p, "default", "c", core.SayParams{
		Agent: "bob", Body: "re", ExpectedLastSeq: ptr(last + 1), ReplyTo: ptr(last + 1),
	}))
	if ok.Message.ReplyTo == nil || *ok.Message.ReplyTo != last+1 {
		t.Fatalf("reply = %+v", ok.Message)
	}
}

func TestConcurrentSaysGetDistinctContiguousSeqs(t *testing.T) {
	f := newFixture(t, core.Options{})
	agents := []string{"a1", "a2", "a3", "a4"}
	f.start(t, "c", core.EndNone, agents...)
	base := f.get(t, "c").LastSeq

	const perAgent = 10
	var wg sync.WaitGroup
	errs := make(chan error, len(agents)*perAgent)
	for _, a := range agents {
		wg.Go(func() {
			for range perAgent {
				_, err := f.svc.Say(context.Background(), f.p, "default", "c", core.SayParams{Agent: a, Body: "x"})
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	r := must[core.ReadResult](t)(f.svc.Read(t.Context(), f.p, "default", "c", core.ReadParams{After: ptr(base), Limit: core.MaxReadLimit}))
	if len(r.Messages) != len(agents)*perAgent {
		t.Fatalf("got %d messages", len(r.Messages))
	}
	for i, m := range r.Messages {
		if m.Seq != base+int64(i)+1 {
			t.Fatalf("message %d has seq %d", i, m.Seq)
		}
	}
}

func TestReadAdvancesCursorAndPages(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	for range 3 {
		f.say(t, "c", "alice", "m")
	}

	// bob's unread covers alice's messages and system lines; bob's own join
	// line is authored by bob and excluded.
	st := must[core.Standing](t)(f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "bob", Resume: true}))
	if st.Unread == 0 || st.Next != core.NextRead {
		t.Fatalf("standing = %+v", st)
	}

	r1 := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "bob", Limit: 2}))
	if len(r1.Messages) != 2 || !r1.More {
		t.Fatalf("page 1 = %+v", r1)
	}
	r2 := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "bob", Limit: core.MaxReadLimit}))
	if len(r2.Messages) == 0 || r2.More || r2.Messages[0].Seq <= r1.Messages[1].Seq {
		t.Fatalf("page 2 = %+v", r2)
	}
	for _, m := range append(r1.Messages, r2.Messages...) {
		if m.Author == "bob" {
			t.Fatalf("default read returned bob's own message: %+v", m)
		}
	}
	if r2.Standing == nil || r2.Standing.Unread != 0 || r2.Standing.Next != core.NextWait {
		t.Fatalf("standing = %+v", r2.Standing)
	}

	// An explicit After re-reads without moving the cursor backwards.
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "bob", After: ptr(int64(0)), Limit: 1}))
	if got := participant(t, f.get(t, "c"), "bob").ReadSeq; got != r2.LastSeq {
		t.Fatalf("read_seq = %d, want %d", got, r2.LastSeq)
	}

	_, err := f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Limit: core.MaxReadLimit + 1})
	wantCode(t, err, core.CodeInvalid)
	_, err = f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "carol"})
	wantCode(t, err, core.CodeNotParticipant)
}

func TestRepliesToYou(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	q := f.say(t, "c", "alice", "question?")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))

	must[core.SayResult](t)(f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: "bob", Body: "answer", ReplyTo: &q.Message.Seq}))
	f.say(t, "c", "bob", "aside")

	sts := must[[]core.Standing](t)(f.svc.Status(ctx, f.p, "default", "alice"))
	if len(sts) != 1 || sts[0].Unread != 2 || sts[0].RepliesToYou != 1 {
		t.Fatalf("status = %+v", sts)
	}
	if !strings.Contains(sts[0].Line, "1 reply to you") || !strings.Contains(sts[0].Line, "next: read") {
		t.Fatalf("line = %q", sts[0].Line)
	}
}

func TestAgreementLifecycle(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice", "bob")

	st := must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "AGREED: abc123"}))
	if st.You != core.YouConcluded || st.Expected || st.Status != core.StatusOpen {
		t.Fatalf("after alice concludes: %+v", st)
	}

	// Saying again withdraws the conclusion.
	res := f.say(t, "c", "alice", "wait, one more thing")
	if res.Standing.You != core.YouActive || !res.Standing.Expected {
		t.Fatalf("after withdraw: %+v", res.Standing)
	}

	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "AGREED: def456"}))
	st = must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "bob", Body: "AGREED: def456"}))
	// bob has not read alice's messages yet, so he is told to read first.
	if st.Status != core.StatusConcluded || st.Next != core.NextRead {
		t.Fatalf("after both conclude: %+v", st)
	}
	if c := f.get(t, "c"); c.ConcludedAt == nil {
		t.Fatalf("concluded_at unset: %+v", c)
	}

	_, err := f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: "bob", Body: "late"})
	wantCode(t, err, core.CodeConversationConcluded)
	_, err = f.svc.Observe(ctx, f.p, "default", "c", core.ObserveParams{Name: "human", Body: "late"})
	wantCode(t, err, core.CodeConversationConcluded)
	_, err = f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "carol"})
	wantCode(t, err, core.CodeConversationConcluded)

	// Leaving a concluded conversation is allowed and changes nothing.
	st = must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "bob"}))
	if st.You != core.YouConcluded {
		t.Fatalf("leave after conclusion: %+v", st)
	}
}

func TestLeavingWhileExpectedIsDropped(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "host", "alice", "bob")

	// The creator may step away without dropping.
	st := must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "host", Body: "kickoff done, leaving"}))
	if st.You != core.YouLeft {
		t.Fatalf("host: %+v", st)
	}

	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "done"}))
	st = must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "bob"}))
	if st.You != core.YouDropped {
		t.Fatalf("bob: %+v", st)
	}
	// With bob dropped and alice concluded, nobody is active.
	if c := f.get(t, "c"); c.Status != core.StatusConcluded {
		t.Fatalf("status = %q", c.Status)
	}

	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Limit: core.MaxReadLimit}))
	var sawDrop bool
	for _, m := range r.Messages {
		if m.Kind == core.KindSystem && m.Author == "bob" && strings.Contains(m.Body, "dropped") {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatalf("no drop in log: %+v", r.Messages)
	}
}

func TestLeavingWithoutEndConditionIsNotDropped(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndNone, "alice", "bob")
	st := must[core.Standing](t)(f.svc.Leave(t.Context(), f.p, "default", "c", core.LeaveParams{Agent: "bob"}))
	if st.You != core.YouLeft {
		t.Fatalf("standing = %+v", st)
	}
}

func TestCoordinatorClose(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndCoordinator, "coord", "alice")

	_, err := f.svc.Close(ctx, f.p, "default", "c", core.CloseParams{Agent: "alice"})
	wantCode(t, err, core.CodeNotCoordinator)

	// Concluding does not end a coordinator conversation.
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "ready"}))
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "coord", Body: "ok"}))
	if c := f.get(t, "c"); c.Status != core.StatusOpen {
		t.Fatalf("status = %q", c.Status)
	}

	st := must[core.Standing](t)(f.svc.Close(ctx, f.p, "default", "c", core.CloseParams{Agent: "coord", Body: "merged"}))
	if st.Status != core.StatusConcluded {
		t.Fatalf("standing = %+v", st)
	}
	_, err = f.svc.Close(ctx, f.p, "default", "c", core.CloseParams{Agent: "coord"})
	wantCode(t, err, core.CodeConversationConcluded)
}

func TestTimeboxExpiresLazily(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	ends := f.clock.Now().Add(time.Hour)
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{
		Name: "c", Agent: "alice", Join: true, End: core.EndTimebox, EndsAt: &ends,
	}))
	f.say(t, "c", "alice", "before")

	f.clock.Advance(time.Hour)
	if c := f.get(t, "c"); c.Status != core.StatusConcluded {
		t.Fatalf("status = %q", c.Status)
	}
	_, err := f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: "alice", Body: "after"})
	wantCode(t, err, core.CodeConversationConcluded)

	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Limit: core.MaxReadLimit}))
	last := r.Messages[len(r.Messages)-1]
	if last.Author != core.SystemAuthor || !strings.Contains(last.Body, "timebox elapsed") {
		t.Fatalf("last = %+v", last)
	}
}

func TestObserverDoesNotJoin(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice"}))

	m := must[core.Message](t)(f.svc.Observe(ctx, f.p, "default", "c", core.ObserveParams{Name: "pat", Body: "consider X"}))
	if m.Kind != core.KindObserver || m.Author != "pat" {
		t.Fatalf("message = %+v", m)
	}
	c := f.get(t, "c")
	if len(c.Participants) != 1 {
		t.Fatalf("participants = %+v", c.Participants)
	}
	sts := must[[]core.Standing](t)(f.svc.Status(ctx, f.p, "default", "alice"))
	if sts[0].Unread != 1 {
		t.Fatalf("status = %+v", sts)
	}
}

func TestPresenceAndStale(t *testing.T) {
	f := newFixture(t, core.Options{StaleAfter: time.Minute})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")

	back := f.clock.Now().Add(30 * time.Minute)
	pt := must[core.Participant](t)(f.svc.SetPresence(ctx, f.p, "default", "c", core.PresenceParams{
		Agent: "alice", Presence: core.PresenceAway, Note: "running the suite", CheckBackBy: &back,
	}))
	if pt.Presence != core.PresenceAway || pt.Note != "running the suite" || pt.CheckBackBy == nil || !pt.CheckBackBy.Equal(back) {
		t.Fatalf("participant = %+v", pt)
	}
	_, err := f.svc.SetPresence(ctx, f.p, "default", "c", core.PresenceParams{Agent: "alice", Presence: "asleep"})
	wantCode(t, err, core.CodeInvalid)

	f.clock.Advance(2 * time.Minute)
	f.say(t, "c", "bob", "still here")
	c := f.get(t, "c")
	if !participant(t, c, "alice").Stale || participant(t, c, "bob").Stale {
		t.Fatalf("participants = %+v", c.Participants)
	}
}

func names(page core.ListPage) string {
	var ns []string
	for _, c := range page.Conversations {
		ns = append(ns, c.Name)
	}
	return strings.Join(ns, ",")
}

func TestListArchivesQuietConversations(t *testing.T) {
	f := newFixture(t, core.Options{ArchiveAfter: 24 * time.Hour})
	ctx := t.Context()
	f.start(t, "old", core.EndNone, "alice")
	f.clock.Advance(48 * time.Hour)
	f.start(t, "new", core.EndNone, "alice")

	page := must[core.ListPage](t)(f.svc.ListConversations(ctx, f.p, "default", core.ListParams{}))
	if names(page) != "new" {
		t.Fatalf("list = %s", names(page))
	}
	page = must[core.ListPage](t)(f.svc.ListConversations(ctx, f.p, "default", core.ListParams{IncludeArchived: true}))
	if names(page) != "new,old" || !page.Conversations[1].Archived {
		t.Fatalf("list = %+v", page)
	}
}

func TestListFiltersByStatus(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "done", core.EndAgreement, "alice")
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "done", core.ConcludeParams{Agent: "alice", Body: "ok"}))
	must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "done", core.LeaveParams{Agent: "alice"}))
	f.clock.Advance(time.Minute)
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{
		Name: "boxed", End: core.EndTimebox, EndsAt: ptr(f.clock.Now().Add(time.Hour)), Agent: "alice", Join: true,
	}))
	f.clock.Advance(time.Minute)
	f.start(t, "live", core.EndNone, "alice")

	for status, want := range map[core.Status]string{"": "live,boxed,done", core.StatusOpen: "live,boxed", core.StatusConcluded: "done"} {
		page := must[core.ListPage](t)(f.svc.ListConversations(ctx, f.p, "default", core.ListParams{Status: status}))
		if names(page) != want {
			t.Errorf("status %q: %s, want %s", status, names(page), want)
		}
	}
	// A timebox past its end counts as concluded before anyone records it.
	f.clock.Advance(2 * time.Hour)
	page := must[core.ListPage](t)(f.svc.ListConversations(ctx, f.p, "default", core.ListParams{Status: core.StatusOpen, IncludeArchived: true}))
	if names(page) != "live" {
		t.Errorf("open after the timebox: %s", names(page))
	}
	page = must[core.ListPage](t)(f.svc.ListConversations(ctx, f.p, "default", core.ListParams{Status: core.StatusConcluded, IncludeArchived: true}))
	if !strings.Contains(names(page), "boxed") {
		t.Errorf("concluded after the timebox: %s", names(page))
	}
	_, err := f.svc.ListConversations(ctx, f.p, "default", core.ListParams{Status: "gone"})
	wantCode(t, err, core.CodeInvalid)
}

func TestListPages(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	var want []string
	for i := range 7 {
		name := fmt.Sprintf("c%d", i)
		f.start(t, name, core.EndNone, "alice")
		want = append([]string{name}, want...)
		if i%2 == 0 {
			f.clock.Advance(time.Second) // some share a last_at; the id breaks ties
		}
	}
	var got []string
	before := ""
	for range 10 {
		page := must[core.ListPage](t)(f.svc.ListConversations(ctx, f.p, "default", core.ListParams{Limit: 3, Before: before}))
		if len(page.Conversations) > 3 {
			t.Fatalf("page of %d", len(page.Conversations))
		}
		for _, c := range page.Conversations {
			got = append(got, c.Name)
		}
		if page.Next == "" {
			break
		}
		before = page.Next
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pages = %v, want %v", got, want)
	}
	for _, bad := range []core.ListParams{{Before: "nonsense"}, {Limit: -1}, {Limit: core.MaxListLimit + 1}} {
		_, err := f.svc.ListConversations(ctx, f.p, "default", bad)
		wantCode(t, err, core.CodeInvalid)
	}
}

func TestStatusAcrossConversations(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "one", core.EndAgreement, "alice", "bob")
	f.start(t, "two", core.EndNone, "carol", "alice")
	f.start(t, "three", core.EndNone, "carol")
	must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "two", core.LeaveParams{Agent: "alice"}))

	sts := must[[]core.Standing](t)(f.svc.Status(ctx, f.p, "default", "alice"))
	if len(sts) != 1 || sts[0].Conversation != "one" || !sts[0].Expected {
		t.Fatalf("status = %+v", sts)
	}
}

func TestWaitReturnsUnreadImmediately(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndAgreement, "alice", "bob")
	f.say(t, "c", "bob", "first line\nsecond line")

	d := must[core.Digest](t)(f.svc.Wait(t.Context(), f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: time.Minute}))
	if d.TimedOut || d.New == 0 {
		t.Fatalf("digest = %+v", d)
	}
	last := d.Previews[len(d.Previews)-1]
	if last.Author != "bob" || last.Text != "first line" {
		t.Fatalf("preview = %+v", last)
	}
	if d.Standing.Next != core.NextRead {
		t.Fatalf("standing = %+v", d.Standing)
	}
}

func TestWaitBlocksUntilAnotherAuthorPosts(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))

	type result struct {
		d   core.Digest
		err error
	}
	done := make(chan result, 1)
	go func() {
		d, err := f.svc.Wait(context.Background(), f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: 10 * time.Second})
		done <- result{d, err}
	}()

	// alice's own post must not wake her.
	time.Sleep(50 * time.Millisecond)
	f.say(t, "c", "alice", "mine")
	select {
	case r := <-done:
		t.Fatalf("woke on own message: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	f.say(t, "c", "bob", "yours")
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.d.TimedOut || r.d.New != 1 || r.d.Authors[0].Author != "bob" {
			t.Fatalf("digest = %+v", r.d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not wake")
	}
}

func TestWaitTimesOutAsNormalResult(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice"}))

	d := must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: 20 * time.Millisecond}))
	if !d.TimedOut || d.New != 0 || d.Standing.Next != core.NextWait || !d.Standing.Expected {
		t.Fatalf("digest = %+v", d)
	}
	if !strings.Contains(d.Standing.Line, "do not end your turn") {
		t.Fatalf("line = %q", d.Standing.Line)
	}
}

func TestWaitSetsPresence(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))

	go func() {
		_, _ = f.svc.Wait(context.Background(), f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: 300 * time.Millisecond})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for participant(t, f.get(t, "c"), "alice").Presence != core.PresenceWaiting {
		if time.Now().After(deadline) {
			t.Fatal("presence never became waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWaitWakesOnConclusionAndCancel(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndCoordinator, "coord", "alice")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))

	waitCtx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := f.svc.Wait(waitCtx, f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: 10 * time.Second})
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait ignored cancel")
	}

	done := make(chan core.Digest, 1)
	go func() {
		d, _ := f.svc.Wait(context.Background(), f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: 10 * time.Second})
		done <- d
	}()
	time.Sleep(50 * time.Millisecond)
	must[core.Standing](t)(f.svc.Close(ctx, f.p, "default", "c", core.CloseParams{Agent: "coord"}))
	select {
	case d := <-done:
		if d.Standing.Status != core.StatusConcluded || d.New != 1 || d.Standing.Next != core.NextRead {
			t.Fatalf("digest = %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not wake on close")
	}
}

type denyAll struct{}

func (denyAll) Authorize(context.Context, core.Principal, string, core.Action) error {
	return &core.Error{Code: core.CodeForbidden, Message: "denied"}
}

func TestPolicyIsConsulted(t *testing.T) {
	f := newFixture(t, core.Options{Policy: denyAll{}})
	ctx := t.Context()
	p := f.p
	calls := map[string]func() error{
		"CreateNamespace": func() error { _, err := f.svc.CreateNamespace(ctx, p, "x"); return err },
		"ListConversations": func() error {
			_, err := f.svc.ListConversations(ctx, p, "default", core.ListParams{})
			return err
		},
		"GetConversation": func() error { _, err := f.svc.GetConversation(ctx, p, "default", "c"); return err },
		"Start": func() error {
			_, err := f.svc.Start(ctx, p, "default", core.StartParams{Name: "c", Agent: "a"})
			return err
		},
		"Join":  func() error { _, err := f.svc.Join(ctx, p, "default", "c", core.JoinParams{Agent: "a"}); return err },
		"Leave": func() error { _, err := f.svc.Leave(ctx, p, "default", "c", core.LeaveParams{Agent: "a"}); return err },
		"Say": func() error {
			_, err := f.svc.Say(ctx, p, "default", "c", core.SayParams{Agent: "a", Body: "x"})
			return err
		},
		"Conclude": func() error {
			_, err := f.svc.Conclude(ctx, p, "default", "c", core.ConcludeParams{Agent: "a", Body: "x"})
			return err
		},
		"Close": func() error { _, err := f.svc.Close(ctx, p, "default", "c", core.CloseParams{Agent: "a"}); return err },
		"Observe": func() error {
			_, err := f.svc.Observe(ctx, p, "default", "c", core.ObserveParams{Name: "a", Body: "x"})
			return err
		},
		"SetPresence": func() error {
			_, err := f.svc.SetPresence(ctx, p, "default", "c", core.PresenceParams{Agent: "a", Presence: core.PresenceAway})
			return err
		},
		"Read":   func() error { _, err := f.svc.Read(ctx, p, "default", "c", core.ReadParams{}); return err },
		"Wait":   func() error { _, err := f.svc.Wait(ctx, p, "default", "c", core.WaitParams{Agent: "a"}); return err },
		"Status": func() error { _, err := f.svc.Status(ctx, p, "default", "a"); return err },
		"Follow": func() error {
			_, err := f.svc.Follow(ctx, p, "default", "c", 0, time.Millisecond)
			return err
		},
		"StandingOf": func() error {
			_, err := f.svc.StandingOf(ctx, p, "default", "c", "a")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { wantCode(t, call(), core.CodeForbidden) })
	}

	ns := must[[]core.Namespace](t)(f.svc.ListNamespaces(ctx, p))
	if len(ns) != 0 {
		t.Fatalf("namespaces visible under deny: %+v", ns)
	}
}

func TestTimeboxExpiryPersistsWhenTheTriggeringWriteFails(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	ends := f.clock.Now().Add(time.Hour)
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{
		Name: "c", Agent: "alice", Join: true, End: core.EndTimebox, EndsAt: &ends,
	}))
	before := f.get(t, "c").LastSeq
	f.clock.Advance(time.Hour)

	// The first access after the deadline is a write, which is rejected.
	_, err := f.svc.Say(ctx, f.p, "default", "c", core.SayParams{Agent: "alice", Body: "late"})
	wantCode(t, err, core.CodeConversationConcluded)

	rejectedAt := f.clock.Now()
	f.clock.Advance(time.Hour)

	// The expiry was recorded by the rejected write, not rolled back with it
	// and re-recorded by this later read.
	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{After: &before}))
	if len(r.Messages) != 1 || !strings.Contains(r.Messages[0].Body, "timebox elapsed") {
		t.Fatalf("messages after deadline = %+v", r.Messages)
	}
	if !r.Messages[0].CreatedAt.Equal(rejectedAt) {
		t.Fatalf("expiry recorded at %s, want %s", r.Messages[0].CreatedAt, rejectedAt)
	}
}

func TestConcludedWithUnreadSaysRead(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "AGREED"}))
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "bob", Body: "AGREED, with caveat X"}))

	d := must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "alice", Timeout: time.Second}))
	if d.Standing.Status != core.StatusConcluded || d.Standing.Next != core.NextRead || d.Standing.Unread == 0 {
		t.Fatalf("standing = %+v", d.Standing)
	}
	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	if r.Standing.Next != core.NextDone {
		t.Fatalf("after reading: %+v", r.Standing)
	}
}

func TestCoordinatorIsToldToClose(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{
		Name: "c", Agent: "coord", Join: true, End: core.EndCoordinator,
	}))
	must[core.Standing](t)(f.svc.Join(ctx, f.p, "default", "c", core.JoinParams{Agent: "alice"}))
	st := must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "coord", Body: "kickoff: do X"}))
	if st.You != core.YouLeft || !strings.Contains(st.Line, "you started this conversation") {
		t.Fatalf("coordinator after stepping away: %+v", st)
	}
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "X done"}))

	// The creator stepped away, so status must still surface the conversation.
	sts := must[[]core.Standing](t)(f.svc.Status(ctx, f.p, "default", "coord"))
	if len(sts) != 1 || sts[0].Next != core.NextClose {
		t.Fatalf("status = %+v", sts)
	}
	st = must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "still done"}))
	if st.Next == core.NextClose {
		t.Fatalf("non-creator told to close: %+v", st)
	}
}

func TestLeaveAfterConclusionIsANoOp(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndCoordinator, "coord", "alice")
	must[core.Standing](t)(f.svc.Close(ctx, f.p, "default", "c", core.CloseParams{Agent: "coord"}))
	last := f.get(t, "c").LastSeq

	_, err := f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "alice", Body: "bye"})
	wantCode(t, err, core.CodeConversationConcluded)

	st := must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "alice"}))
	if st.You != core.YouActive || st.Status != core.StatusConcluded {
		t.Fatalf("standing = %+v", st)
	}
	c := f.get(t, "c")
	if c.LastSeq != last || participant(t, c, "alice").State != core.StateActive {
		t.Fatalf("leave after conclusion changed state: %+v", c)
	}
}

func TestLeavingWithAMessageWithdrawsAConclusion(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "host", "alice", "bob")
	must[core.Standing](t)(f.svc.Conclude(ctx, f.p, "default", "c", core.ConcludeParams{Agent: "alice", Body: "AGREED"}))

	st := must[core.Standing](t)(f.svc.Leave(ctx, f.p, "default", "c", core.LeaveParams{Agent: "alice", Body: "I withdraw my approval"}))
	if st.You != core.YouDropped {
		t.Fatalf("standing = %+v", st)
	}
	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Limit: core.MaxReadLimit}))
	var withdrew bool
	for _, m := range r.Messages {
		if m.Author == "alice" && strings.Contains(m.Body, "withdrew") {
			withdrew = true
		}
	}
	if !withdrew {
		t.Fatalf("no withdrawal in log: %+v", r.Messages)
	}
}

func TestWaitersAreCapped(t *testing.T) {
	f := newFixture(t, core.Options{MaxWaiters: 2})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "a1", "a2", "a3")
	for _, a := range []string{"a1", "a2", "a3"} {
		must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: a, Limit: core.MaxReadLimit}))
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for _, a := range []string{"a1", "a2"} {
		wg.Go(func() {
			_, _ = f.svc.Wait(waitCtx, f.p, "default", "c", core.WaitParams{Agent: a, Timeout: 10 * time.Second})
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c := f.get(t, "c")
		if participant(t, c, "a1").Presence == core.PresenceWaiting && participant(t, c, "a2").Presence == core.PresenceWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiters never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err := f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "a3", Timeout: time.Second})
	wantCode(t, err, core.CodeTooManyWaiters)
	cancel()
	wg.Wait()

	// Slots are released when waits end.
	must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "a3"}))
}

func TestStandingOf(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice")
	f.say(t, "c", "alice", "hello")

	st := must[core.Standing](t)(f.svc.StandingOf(ctx, f.p, "default", "c", "alice"))
	if st.You != core.YouActive || !st.Expected {
		t.Fatalf("alice = %+v", st)
	}
	st = must[core.Standing](t)(f.svc.StandingOf(ctx, f.p, "default", "c", "bob"))
	if st.You != core.YouNotJoined || st.Next != core.NextJoin {
		t.Fatalf("bob = %+v", st)
	}
	_, err := f.svc.StandingOf(ctx, f.p, "default", "nope", "bob")
	wantCode(t, err, core.CodeConversationNotFound)
}

func TestCoordinatorWhoNeverJoinedGetsCoordinatorAdvice(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{Name: "c", Agent: "coord", End: core.EndCoordinator}))
	sts := must[[]core.Standing](t)(f.svc.Status(ctx, f.p, "default", "coord"))
	if len(sts) != 1 || sts[0].You != core.YouNotJoined || !strings.Contains(sts[0].Line, "you started this conversation") {
		t.Fatalf("status = %+v", sts)
	}
}

func TestReadSkipsOwnMessagesUnlessAfterIsGiven(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	mine := f.say(t, "c", "alice", "mine")
	theirs := f.say(t, "c", "bob", "theirs")
	f.say(t, "c", "alice", "mine again")

	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice"}))
	if len(r.Messages) != 1 || r.Messages[0].Seq != theirs.Message.Seq || r.More {
		t.Fatalf("default read = %+v", r.Messages)
	}
	// The read position covers alice's own trailing message too.
	if got := participant(t, f.get(t, "c"), "alice").ReadSeq; got != r.LastSeq {
		t.Fatalf("read_seq = %d, want %d", got, r.LastSeq)
	}

	r = must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", After: ptr(mine.Message.Seq - 1)}))
	if len(r.Messages) != 3 || r.Messages[0].Seq != mine.Message.Seq {
		t.Fatalf("read with after = %+v", r.Messages)
	}
}

func TestReadPagesWhenSkippingOwnMessages(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	for range 3 {
		f.say(t, "c", "bob", "b")
		f.say(t, "c", "alice", "a")
	}
	r1 := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: 2}))
	if len(r1.Messages) != 2 || !r1.More {
		t.Fatalf("page 1 = %+v", r1)
	}
	r2 := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: 2}))
	if len(r2.Messages) != 1 || r2.More || r2.Standing.Unread != 0 {
		t.Fatalf("page 2 = %+v", r2)
	}
}

func TestWaitInlinesShortMessages(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	f.say(t, "c", "alice", "mine")
	f.say(t, "c", "bob", "one")
	f.say(t, "c", "bob", "two")

	d := must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "alice", InlineBytes: 100}))
	if len(d.Messages) != 2 || d.New != 2 || len(d.Previews) != 0 || d.Authors[0] != (core.AuthorCount{Author: "bob", Count: 2}) {
		t.Fatalf("digest = %+v", d)
	}
	if d.Standing.Unread != 0 || d.Standing.Next != core.NextWait {
		t.Fatalf("standing = %+v", d.Standing)
	}
	if got := participant(t, f.get(t, "c"), "alice"); got.ReadSeq != d.LastSeq || got.Presence != core.PresenceWorking {
		t.Fatalf("participant = %+v", got)
	}
}

func TestWaitDoesNotInlineOverBudgetOrWithAfter(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndAgreement, "alice", "bob")
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	before := participant(t, f.get(t, "c"), "alice").ReadSeq
	f.say(t, "c", "bob", "a message that is longer than the budget")

	d := must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "alice", InlineBytes: 10}))
	if len(d.Messages) != 0 || len(d.Previews) != 1 || d.Standing.Next != core.NextRead {
		t.Fatalf("over budget = %+v", d)
	}
	d = must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "alice", InlineBytes: 1000, After: &before}))
	if len(d.Messages) != 0 || len(d.Previews) != 1 {
		t.Fatalf("with after = %+v", d)
	}
	if got := participant(t, f.get(t, "c"), "alice").ReadSeq; got != before {
		t.Fatalf("read_seq moved to %d", got)
	}
}

func TestFollow(t *testing.T) {
	f := newFixture(t, core.Options{MaxWatchers: 1, MaxWaiters: 1})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	before := f.get(t, "c")

	// Behind: returns at once, everything after `after`, all authors.
	r := must[core.ReadResult](t)(f.svc.Follow(ctx, f.p, "default", "c", 0, time.Minute))
	if int64(len(r.Messages)) != before.LastSeq {
		t.Fatalf("follow from 0 = %+v", r)
	}

	// Caught up: times out empty.
	r = must[core.ReadResult](t)(f.svc.Follow(ctx, f.p, "default", "c", before.LastSeq, 20*time.Millisecond))
	if len(r.Messages) != 0 || r.LastSeq != before.LastSeq {
		t.Fatalf("idle follow = %+v", r)
	}

	// Caught up: wakes on a post. A viewer does not use the agents' wait slot.
	done := make(chan core.ReadResult, 1)
	go func() {
		r, _ := f.svc.Follow(context.Background(), f.p, "default", "c", before.LastSeq, 10*time.Second)
		done <- r
	}()
	time.Sleep(50 * time.Millisecond)
	_, err := f.svc.Follow(ctx, f.p, "default", "c", before.LastSeq, time.Millisecond)
	wantCode(t, err, core.CodeTooManyWatchers)
	must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "alice", Limit: core.MaxReadLimit}))
	must[core.Digest](t)(f.svc.Wait(ctx, f.p, "default", "c", core.WaitParams{Agent: "alice"}))
	f.say(t, "c", "bob", "hello")
	select {
	case r := <-done:
		if len(r.Messages) != 1 || r.Messages[0].Body != "hello" {
			t.Fatalf("woken follow = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not wake")
	}

	// Following changes nothing about participants.
	after := f.get(t, "c")
	for i, p := range after.Participants {
		if p.ReadSeq != before.Participants[i].ReadSeq && p.Agent != "alice" {
			t.Fatalf("follow moved %s's read position", p.Agent)
		}
	}
}

func TestFollowWakesAtTimeboxDeadline(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	ends := f.clock.Now().Add(300 * time.Millisecond)
	must[core.Conversation](t)(f.svc.Start(ctx, f.p, "default", core.StartParams{Name: "c", Agent: "alice", Join: true, End: core.EndTimebox, EndsAt: &ends}))
	last := f.get(t, "c").LastSeq
	// Nothing else touches the conversation; only the deadline passes.
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.clock.Advance(time.Second)
	}()
	start := time.Now()
	r := must[core.ReadResult](t)(f.svc.Follow(ctx, f.p, "default", "c", last, 10*time.Second))
	if time.Since(start) > 3*time.Second {
		t.Fatalf("follow slept past the deadline: %s", time.Since(start))
	}
	if len(r.Messages) != 1 || !strings.Contains(r.Messages[0].Body, "timebox elapsed") {
		t.Fatalf("follow at deadline = %+v", r)
	}
}

// A cursor past the end of the log is refused instead of moving the read
// position beyond messages that have not been written yet.
func TestReadCursorCannotPassTheLog(t *testing.T) {
	f := newFixture(t, core.Options{})
	ctx := t.Context()
	f.start(t, "c", core.EndNone, "alice", "bob")
	_, err := f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "bob", After: ptr(int64(1) << 62)})
	wantCode(t, err, core.CodeInvalid)
	f.say(t, "c", "alice", "still delivered")
	r := must[core.ReadResult](t)(f.svc.Read(ctx, f.p, "default", "c", core.ReadParams{Agent: "bob"}))
	found := false
	for _, m := range r.Messages {
		found = found || m.Body == "still delivered"
	}
	if !found {
		t.Fatalf("the next message was skipped: %+v", r.Messages)
	}
}
