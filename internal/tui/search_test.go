package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aahoughton/agora/internal/core"
)

// From the conversation list, s searches the namespace; enter on a hit
// opens its conversation at that message, even one older than the
// conversation shows by default, and esc comes back to the results.
func TestSearchScreen(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", ShownMessages: 5})
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "design", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Say(ctx, p, "default", "design", core.SayParams{Agent: "alice", Body: "the eviction policy is LRU"}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := h.svc.Say(ctx, p, "default", "design", core.SayParams{Agent: "alice", Body: fmt.Sprintf("filler %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	h.start()
	h.until("the conversation list", h.shows("design"))

	h.keys("s")
	h.until("the search prompt", h.shows("Type words"))
	// q is text while typing, not quit.
	h.typeText("eviction q")
	h.keys("backspace", "backspace", "enter")
	h.until("the results", h.shows("1 message matches"))
	if !h.shows("design #3")() || !h.shows("the eviction policy is LRU")() {
		t.Fatalf("hit not shown:\n%s", ansi.Strip(h.m.View().Content))
	}

	h.keys("enter")
	h.until("the conversation at the hit", func() bool {
		return h.m.t != nil && h.m.t.caughtUp
	})
	view := ansi.Strip(h.m.t.vp.View())
	if first := strings.SplitN(view, "\n", 2)[0]; !strings.Contains(first, "#3") {
		t.Fatalf("the hit is not at the top:\n%s", view)
	}
	if h.m.t.newBelow != 0 {
		t.Fatalf("the backlog counts as %d new", h.m.t.newBelow)
	}

	h.keys("esc")
	h.until("the results again", h.shows("1 message matches"))
	h.keys("o")
	h.until("the order", h.shows("by newest"))
	h.until("the results in the new order", h.shows("design #3"))

	h.keys("s")
	for range len("eviction") {
		h.keys("backspace")
	}
	h.typeText("nothing")
	h.keys("enter")
	h.until("no results", h.shows("No messages match."))

	h.keys("esc")
	h.until("the conversation list again", h.shows("Conversations in default"))
	if h.m.search != nil {
		t.Fatal("the search outlived its screen")
	}
}

func TestSearchView(t *testing.T) {
	m := newTestModel(t, Options{Namespace: "default"}, 90, 12)
	m.Update(conversationsMsg{gen: m.gen, page: core.ListPage{Conversations: []core.Conversation{sampleConversation()}}})
	m.Update(press("s"))
	for _, r := range "retry" {
		m.Update(press(string(r)))
	}
	m.Update(press("enter"))
	m.Update(searchMsg{gen: m.gen, page: core.SearchPage{Total: 3, Next: "r2", Hits: []core.SearchHit{
		{
			Conversation: "api-shape", Seq: 4, Kind: core.KindSay, Author: "claude-implementer", CreatedAt: at(2),
			Snippet: []core.SnippetPart{{Text: "And "}, {Text: "retry", Match: true}, {Text: "_after on 429."}},
		},
		{
			Conversation: "api-shape", Seq: 9, Kind: core.KindObserver, Author: "carol", CreatedAt: at(10),
			Snippet: []core.SnippetPart{{Text: "The CLI already "}, {Text: "retries", Match: true}, {Text: " on 503, and this line runs on well past the width of the screen."}},
		},
	}}})
	golden(t, "search", shot(m))
}

// A snippet is untrusted text: terminal controls and bidirectional
// overrides in a body do not reach the screen, matched or not.
func TestSearchSnippetIsSanitized(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default"})
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "c", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Say(ctx, p, "default", "c", core.SayParams{Agent: "alice", Body: "note \x1b]52;c;SGVsbG8=\x07 \u202ereversed"}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the conversation list", h.shows("Conversations in default"))
	for _, q := range []string{"note", "reversed"} {
		h.keys("s")
		for range 10 {
			h.keys("backspace")
		}
		h.typeText(q)
		h.keys("enter")
		h.until("the result for "+q, h.shows("1 message matches"))
		if view := h.m.View().Content; strings.Contains(view, "\x1b]52;") || strings.Contains(view, "\u202e") {
			t.Fatalf("%s: controls reached the screen: %q", q, view)
		}
	}
}

// Leaving the results while a page loads cancels that request; back on
// the results, paging works again.
func TestSearchPagingAfterReturn(t *testing.T) {
	m := newTestModel(t, Options{Namespace: "default"}, 90, 12)
	m.Update(conversationsMsg{gen: m.gen, page: core.ListPage{Conversations: []core.Conversation{sampleConversation()}}})
	m.Update(press("s"))
	m.Update(press("x"))
	m.Update(press("enter"))
	m.Update(searchMsg{gen: m.gen, page: core.SearchPage{Total: 2, Next: "r1", Hits: []core.SearchHit{{Conversation: "c", Seq: 3}}}})
	if m.searchMore() == nil {
		t.Fatal("no request for the next page")
	}
	stale := m.gen
	m.Update(press("enter"))
	m.Update(press("esc"))
	m.Update(searchMsg{gen: stale, more: true, page: core.SearchPage{Total: 2}})
	if m.search.loadingMore || m.searchMore() == nil {
		t.Fatal("paging stays stuck after returning to the results")
	}
}
