package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aahoughton/agora/internal/api"
	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

// harness runs a Model against a real server the way Bubble Tea would:
// commands run in goroutines and their messages are fed back to Update one
// at a time, on the test goroutine.
type harness struct {
	t    *testing.T
	m    *Model
	svc  *core.Service
	srv  *httptest.Server
	msgs chan tea.Msg
}

// newHarness serves the JSON API, through wrap if given, and returns a
// model for it.
func newHarness(t *testing.T, opts Options, wrap ...func(http.Handler) http.Handler) *harness {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := core.New(db, core.Options{})
	mux := http.NewServeMux()
	api.Register(mux, svc, auth.None{}, api.Options{})
	var h0 http.Handler = mux
	for _, w := range wrap {
		h0 = w(h0)
	}
	srv := httptest.NewServer(h0)
	t.Cleanup(srv.Close)
	if opts.Client, err = client.New(srv.URL); err != nil {
		t.Fatal(err)
	}
	opts.Skin = defaultSkin(t)
	opts.FollowTimeout = time.Second
	opts.RefreshEvery = 100 * time.Millisecond
	opts.RetryAfter = 50 * time.Millisecond
	h := &harness{t: t, svc: svc, srv: srv, msgs: make(chan tea.Msg, 64)}
	h.m = New(t.Context(), opts)
	return h
}

func (h *harness) start() {
	h.run(h.m.Init())
	h.send(tea.WindowSizeMsg{Width: 110, Height: 40})
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	ctx := h.t.Context()
	go func() {
		msg := cmd()
		select {
		case h.msgs <- msg:
		case <-ctx.Done():
		}
	}()
}

func (h *harness) send(msg tea.Msg) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			h.run(c)
		}
		return
	}
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

func (h *harness) keys(ks ...string) {
	for _, k := range ks {
		h.send(press(k))
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.send(press(string(r)))
	}
}

// until processes messages until cond holds, failing after a few seconds.
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case msg := <-h.msgs:
			h.send(msg)
		case <-tick.C:
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; screen:\n%s", what, ansi.Strip(h.m.View().Content))
		}
	}
}

// await processes messages until ch receives, failing after a few seconds.
func (h *harness) await(what string, ch <-chan struct{}) {
	h.t.Helper()
	got := false
	h.until(what, func() bool {
		select {
		case <-ch:
			got = true
		default:
		}
		return got
	})
}

func (h *harness) shows(s string) func() bool {
	return func() bool { return strings.Contains(ansi.Strip(h.m.View().Content), s) }
}

func TestFollowAndComment(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "design"})
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "design", Topic: "shape", End: core.EndAgreement, Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the conversation", h.shows("alice joined"))

	// Live: a message posted elsewhere shows up without a key press.
	if _, err := h.svc.Say(ctx, p, "default", "design", core.SayParams{Agent: "alice", Body: "first **point**"}); err != nil {
		t.Fatal(err)
	}
	h.until("the live message", h.shows("first point"))

	// The first comment asks for a name and rejects a bad one.
	h.keys("i")
	h.typeText("from the side")
	h.keys("enter")
	h.until("the name prompt", h.shows("Your name"))
	h.typeText("bad name")
	h.keys("enter")
	h.until("the name error", h.shows("at most 64 characters"))
	for range len("bad name") {
		h.keys("backspace")
	}
	h.typeText("carol")
	h.keys("enter")
	h.until("the posted comment", h.shows("carol  "))
	if got := h.m.t.composer.Value(); got != "" {
		t.Fatalf("composer kept %q after sending", got)
	}
	if !h.shows("as carol")() {
		t.Fatal("the name is not shown after sending")
	}

	// Concluding closes the composer.
	if _, err := h.svc.Conclude(ctx, p, "default", "design", core.ConcludeParams{Agent: "alice", Body: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Leave(ctx, p, "default", "design", core.LeaveParams{Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	h.until("the closed composer", h.shows("comments are closed"))

	// esc goes back to the conversation list.
	h.keys("esc")
	h.until("the conversation list", h.shows("Conversations in default"))
	h.until("the listed conversation", h.shows("design  concluded"))
}

func TestErrorsAreNotices(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "nope"})
	h.start()
	h.until("the error notice", h.shows(`error: conversation "nope" not found`))

	h.keys("esc")
	h.until("an empty list", h.shows("No conversations"))
	h.keys("esc")
	h.until("the namespace list", h.shows("Namespaces"))
	h.until("the default namespace", h.shows("default"))
}

func TestFilter(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default"})
	ctx, p := t.Context(), core.Anonymous()
	for _, n := range []string{"alpha", "beta"} {
		if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: n, Agent: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	h.start()
	h.until("the list", h.shows("beta"))
	h.keys("/")
	h.typeText("bet")
	h.keys("enter")
	if h.shows("alpha")() || !h.shows("beta")() {
		t.Fatalf("filter did not apply:\n%s", ansi.Strip(h.m.View().Content))
	}
	h.keys("enter")
	h.until("the opened conversation", h.shows("beta  open"))
}

func TestRetriesWhenTheServerGoes(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "design"})
	if _, err := h.svc.Start(t.Context(), core.Anonymous(), "default", core.StartParams{Name: "design", Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the conversation", h.shows("alice started"))
	h.srv.CloseClientConnections()
	h.srv.Close()
	h.until("the retry notice", h.shows("lost the server"))
	if !h.shows("alice started")() {
		t.Fatal("the thread was cleared while retrying")
	}
}

// stallOnce makes the first request matching match hang until the client
// gives up. It reports the hang's start and end on started and ended, if
// they are not nil.
func stallOnce(match func(*http.Request) bool, started, ended chan<- struct{}) func(http.Handler) http.Handler {
	var once sync.Once
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stall := false
			if match(r) {
				once.Do(func() { stall = true })
			}
			if !stall {
				next.ServeHTTP(w, r)
				return
			}
			if started != nil {
				started <- struct{}{}
			}
			<-r.Context().Done()
			if ended != nil {
				ended <- struct{}{}
			}
		})
	}
}

func isFollow(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/follow") && r.URL.Query().Get("timeout") != "0"
}

func TestFollowRecoversFromAStalledRequest(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "design"}, stallOnce(isFollow, nil, nil))
	h.m.margin = 100 * time.Millisecond
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "design", Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the conversation", h.shows("alice started"))
	// The first long follow hangs past its deadline; the next one must
	// still deliver this message.
	time.Sleep(200 * time.Millisecond)
	if _, err := h.svc.Observe(ctx, p, "default", "design", core.ObserveParams{Name: "carol", Body: "after the stall"}); err != nil {
		t.Fatal(err)
	}
	h.until("the message after the stall", h.shows("after the stall"))
}

func TestSendingFreezesTheComposer(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "design", Observer: "carol"})
	if _, err := h.svc.Start(t.Context(), core.Anonymous(), "default", core.StartParams{Name: "design", Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the conversation", h.shows("alice started"))
	h.keys("i")
	h.typeText("first")
	h.keys("enter")
	// The composer holds still while the post is in flight, so nothing
	// typed now is silently dropped when it clears.
	h.typeText(" more")
	if got := h.m.t.composer.Value(); got != "first" {
		t.Fatalf("composer = %q while sending, want it frozen", got)
	}
	h.until("the posted comment", h.shows("carol  "))
	h.until("the composer to settle", func() bool { return !h.m.t.sending })
	if got := h.m.t.composer.Value(); got != "" {
		t.Fatalf("composer = %q after sending", got)
	}
	if !h.shows("first")() || h.shows("first more")() {
		t.Fatalf("wrong comment posted:\n%s", ansi.Strip(h.m.View().Content))
	}
}

func TestNavigationCancelsRequests(t *testing.T) {
	started, ended := make(chan struct{}, 1), make(chan struct{}, 1)
	isList := func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/conversations") }
	h := newHarness(t, Options{Namespace: "default"}, stallOnce(isList, started, ended))
	h.start()
	h.await("the list request", started)
	h.keys("esc")
	h.until("the namespace list", h.shows("Namespaces"))
	h.await("the stalled list request to be cancelled", ended)
}

func TestListLoadsMoreAtTheEnd(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default"})
	ctx, p := t.Context(), core.Anonymous()
	for i := range listPage + 5 {
		if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: fmt.Sprintf("c%03d", i), Agent: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	h.start()
	h.until("the first page", func() bool { return len(h.m.conversations) == listPage })
	h.keys("G")
	h.until("the next page", func() bool { return len(h.m.conversations) == listPage+5 && h.m.next == "" })
	// A refresh keeps what has been loaded.
	h.until("a refresh", func() bool { return len(h.m.conversations) == listPage+5 && !h.m.loadingMore })
	time.Sleep(3 * h.m.opts.RefreshEvery)
	h.until("the list after refreshes", func() bool { return len(h.m.conversations) == listPage+5 })
}

// holdOnce holds the first matching request until release is closed, then
// serves it.
func holdOnce(match func(*http.Request) bool, started chan<- struct{}, release <-chan struct{}) func(http.Handler) http.Handler {
	var once sync.Once
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hold := false
			if match(r) {
				once.Do(func() { hold = true })
			}
			if hold {
				started <- struct{}{}
				<-release
			}
			next.ServeHTTP(w, r)
		})
	}
}

func seedConversations(t *testing.T, h *harness, n int) {
	t.Helper()
	for i := range n {
		if _, err := h.svc.Start(t.Context(), core.Anonymous(), "default", core.StartParams{Name: fmt.Sprintf("c%03d", i), Agent: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRefreshDuringLoadMoreKeepsPaging(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	isMore := func(r *http.Request) bool { return r.URL.Query().Get("before") != "" }
	h := newHarness(t, Options{Namespace: "default"}, holdOnce(isMore, started, release))
	seedConversations(t, h, listPage+5)
	h.start()
	h.until("the first page", func() bool { return len(h.m.conversations) == listPage })
	h.keys("G")
	h.await("the next-page request", started)
	h.keys("r")
	close(release)
	h.until("the refresh", func() bool { return !h.m.loadingMore })
	h.keys("G")
	h.until("the next page after a refresh", func() bool { return len(h.m.conversations) == listPage+5 })
}

func TestRefreshKeepsMoreThanOneRequestHolds(t *testing.T) {
	var refreshes atomic.Int64
	count := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if strings.HasSuffix(r.URL.Path, "/conversations") && r.URL.Query().Get("before") == "" {
				refreshes.Add(1)
			}
		})
	}
	h := newHarness(t, Options{Namespace: "default"}, count)
	const n = core.MaxListLimit + 50
	seedConversations(t, h, n)
	h.start()
	h.until("the first page", func() bool { return len(h.m.conversations) == listPage })
	for len(h.m.conversations) < n {
		have := len(h.m.conversations)
		h.keys("G")
		h.until("another page", func() bool { return len(h.m.conversations) > have && !h.m.loadingMore })
	}
	seen := refreshes.Load()
	h.until("three refreshes", func() bool { return refreshes.Load() >= seen+3 })
	if len(h.m.conversations) != n || h.m.next != "" {
		t.Fatalf("after refreshes: %d conversations, next %q; want %d and none", len(h.m.conversations), h.m.next, n)
	}
}

func TestScrollingUpLoadsEarlierMessages(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "long", ShownMessages: 5})
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "long", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 { // seqs 3..22
		if _, err := h.svc.Say(ctx, p, "default", "long", core.SayParams{Agent: "alice", Body: fmt.Sprintf("body-%d", i+3)}); err != nil {
			t.Fatal(err)
		}
	}
	h.start()
	h.until("the latest messages", func() bool { return h.m.t != nil && len(h.m.t.messages) == 5 })
	if !h.shows("Earlier messages")() {
		t.Fatal("no earlier-messages notice")
	}
	h.keys("g")
	h.until("the earlier messages", func() bool { return len(h.m.t.messages) == 22 && !h.m.t.loadingEarlier })
	if h.m.t.messages[0].Seq != 1 || h.m.t.truncated {
		t.Fatalf("first seq %d, truncated %v", h.m.t.messages[0].Seq, h.m.t.truncated)
	}
	// The screen stays where it was: on the oldest of the five first shown.
	if !h.shows("body-18")() || h.shows("Earlier messages")() {
		t.Fatalf("screen after loading:\n%s", ansi.Strip(h.m.View().Content))
	}
}
