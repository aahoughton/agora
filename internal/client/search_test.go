package client_test

import (
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/core"
)

// Every search parameter reaches the server: each one here narrows the
// result, so a dropped parameter shows as an extra hit.
func TestSearch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c, svc := newServer(t, core.Options{Now: func() time.Time { return now }})
	ctx := t.Context()
	p := core.Anonymous()
	for _, conv := range []string{"a", "b"} {
		if _, err := svc.Start(ctx, p, "default", core.StartParams{Name: conv, Agent: "alice", Join: true}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(conv, agent, body string) {
		t.Helper()
		if _, err := svc.Join(ctx, p, "default", conv, core.JoinParams{Agent: agent}); err != nil && core.CodeOf(err) != core.CodeNameActive {
			t.Fatal(err)
		}
		if _, err := svc.Say(ctx, p, "default", conv, core.SayParams{Agent: agent, Body: body}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	post("b", "alice", "cache early")
	post("a", "bob", "cache by bob")
	post("a", "alice", "cache in a")
	post("a", "alice", "cache in a again")
	post("a", "alice", "cache late")

	start := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	sp := core.SearchParams{
		Query: "cache", Conversation: "a", Author: "alice",
		Since: &start, Until: ptr(start.Add(2 * time.Minute)),
		Order: core.OrderOldest, Limit: 1,
	}
	page, err := c.Search(ctx, "default", sp)
	if err != nil || page.Total != 2 || len(page.Hits) != 1 || page.Hits[0].Text() != "cache in a" || page.Next == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	sp.Cursor = page.Next
	page, err = c.Search(ctx, "default", sp)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Text() != "cache in a again" || page.Next != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}
}

func ptr[T any](v T) *T { return &v }
