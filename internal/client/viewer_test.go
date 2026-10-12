package client_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/api"
	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

// newServer runs the JSON API over a real database and returns a client
// for it and the service behind it.
func newServer(t *testing.T, opts core.Options) (*client.Client, *core.Service) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := core.New(db, opts)
	mux := http.NewServeMux()
	api.Register(mux, svc, auth.None{}, api.Options{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, svc
}

func TestViewerCalls(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	c, svc := newServer(t, core.Options{
		Now:          func() time.Time { return time.UnixMilli(now.Load()) },
		ArchiveAfter: time.Hour,
	})
	ctx := t.Context()
	p := core.Anonymous()
	if _, err := svc.Start(ctx, p, "default", core.StartParams{Name: "old", Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	now.Add((2 * time.Hour).Milliseconds())
	if _, err := svc.Start(ctx, p, "default", core.StartParams{Name: "design", Topic: "shape", Agent: "alice"}); err != nil {
		t.Fatal(err)
	}

	nss, err := c.ListNamespaces(ctx)
	if err != nil || len(nss) != 1 || nss[0].Name != "default" {
		t.Fatalf("namespaces = %+v, %v", nss, err)
	}
	cs, err := c.ListConversations(ctx, "default", false)
	if err != nil || len(cs) != 1 || cs[0].Name != "design" {
		t.Fatalf("conversations = %+v, %v", cs, err)
	}
	cs, err = c.ListConversations(ctx, "default", true)
	if err != nil || len(cs) != 2 {
		t.Fatalf("with archived = %+v, %v", cs, err)
	}
	conv, err := c.GetConversation(ctx, "default", "design")
	if err != nil || conv.Topic != "shape" {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}

	m, err := c.Observe(ctx, "default", "design", "carol", "context")
	if err != nil || m.Kind != core.KindObserver || m.Seq != conv.LastSeq+1 {
		t.Fatalf("observe = %+v, %v", m, err)
	}
	res, err := c.Follow(ctx, "default", "design", conv.LastSeq, 0)
	if err != nil || len(res.Messages) != 1 || res.Messages[0].Body != "context" || res.Conversation.LastSeq != m.Seq {
		t.Fatalf("follow = %+v, %v", res, err)
	}
	res, err = c.Follow(ctx, "default", "design", m.Seq, time.Second)
	if err != nil || len(res.Messages) != 0 || res.LastSeq != m.Seq {
		t.Fatalf("timed-out follow = %+v, %v", res, err)
	}

	_, err = c.GetConversation(ctx, "default", "nope")
	if e, ok := errors.AsType[*client.Error](err); !ok || e.Code != string(core.CodeConversationNotFound) {
		t.Fatalf("missing conversation err = %v", err)
	}
}
