package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/store"
)

func TestWaitLeavesNoHubEntries(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(db, Options{})
	ctx := t.Context()
	p := Anonymous()

	for _, name := range []string{"one", "two", "three"} {
		if _, err := s.Start(ctx, p, "default", StartParams{Name: name, Agent: "alice", Join: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Read(ctx, p, "default", name, ReadParams{Agent: "alice"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Wait(ctx, p, "default", name, WaitParams{Agent: "alice"}); err != nil {
			t.Fatal(err)
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		_, _ = s.Wait(cctx, p, "default", name, WaitParams{Agent: "alice", Timeout: time.Minute})
		cancel()
	}

	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if len(s.hub.subs) != 0 || len(s.hub.waiters) != 0 {
		t.Fatalf("hub retained entries: subs=%d waiters=%d", len(s.hub.subs), len(s.hub.waiters))
	}
}

func TestReadsStayWithinTheLoadedSnapshot(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(db, Options{})
	ctx := t.Context()
	p := Anonymous()
	if _, err := s.Start(ctx, p, "default", StartParams{Name: "c", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(ctx, p, "default", "c", JoinParams{Agent: "bob"}); err != nil {
		t.Fatal(err)
	}

	snap, err := s.loadConv(ctx, db, "default", "c")
	if err != nil {
		t.Fatal(err)
	}
	// A write lands after the snapshot was taken.
	if _, err := s.Say(ctx, p, "default", "c", SayParams{Agent: "bob", Body: "late"}); err != nil {
		t.Fatal(err)
	}

	page, _, err := s.page(ctx, db, snap, snap.LastSeq-1, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Seq != snap.LastSeq || page.More || page.LastSeq != snap.LastSeq {
		t.Fatalf("page = %+v (snapshot last_seq %d)", page, snap.LastSeq)
	}

	st, err := s.standing(ctx, db, snap, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var unread int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE c.name = 'c' AND m.seq <= ? AND m.author != 'alice'`, snap.LastSeq).Scan(&unread); err != nil {
		t.Fatal(err)
	}
	if st.Unread != unread {
		t.Fatalf("unread = %d, want %d (messages up to the snapshot only)", st.Unread, unread)
	}
}
