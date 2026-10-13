package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/server"
	"github.com/aahoughton/agora/internal/store"
)

func newHandler(t *testing.T) (http.Handler, *core.Service) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := core.New(db, core.Options{})
	return server.New(server.Deps{Service: svc, Authenticator: auth.None{}, Logger: slog.New(slog.DiscardHandler)}), svc
}

type cliResult struct {
	code           int
	stdout, stderr string
}

func runCLI(ctx context.Context, url string, args ...string) cliResult {
	var out, errOut bytes.Buffer
	env := func(k string) string {
		if k == "AGORA_URL" {
			return url
		}
		return ""
	}
	code := run(ctx, args, &out, &errOut, env)
	return cliResult{code, out.String(), errOut.String()}
}

func setup(t *testing.T) (string, *core.Service) {
	t.Helper()
	h, svc := newHandler(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p := core.Anonymous()
	if _, err := svc.Start(t.Context(), p, "default", core.StartParams{Name: "c", Agent: "alice", Join: true, End: core.EndAgreement}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(t.Context(), p, "default", "c", core.JoinParams{Agent: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Read(t.Context(), p, "default", "c", core.ReadParams{Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	return srv.URL, svc
}

func TestHealth(t *testing.T) {
	url, _ := setup(t)
	if r := runCLI(t.Context(), url, "health"); r.code != exitOK || r.stdout != "ok\n" {
		t.Fatalf("health = %+v", r)
	}
	if r := runCLI(t.Context(), "http://127.0.0.1:1", "health"); r.code != exitError {
		t.Fatalf("health on a closed port = %+v", r)
	}
}

func TestStatus(t *testing.T) {
	url, _ := setup(t)
	r := runCLI(t.Context(), url, "status", "--agent", "alice")
	if r.code != exitOK || !strings.HasPrefix(r.stdout, "c: open; you: active, expected") {
		t.Fatalf("status = %+v", r)
	}
	r = runCLI(t.Context(), url, "status", "--agent", "zed")
	if r.code != exitOK || r.stdout != "not expected in any open conversation\n" {
		t.Fatalf("status = %+v", r)
	}
}

func TestWaitWakesOnPost(t *testing.T) {
	url, svc := setup(t)
	done := make(chan cliResult, 1)
	go func() { done <- runCLI(context.Background(), url, "wait", "c", "--agent", "alice") }()

	time.Sleep(100 * time.Millisecond)
	if _, err := svc.Say(t.Context(), core.Anonymous(), "default", "c", core.SayParams{Agent: "bob", Body: "ping"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.code != exitOK || !strings.HasPrefix(r.stdout, "1 new in c (bob x1)\nc: open") || !strings.Contains(r.stdout, "next: read") {
			t.Fatalf("wait = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return")
	}
}

func TestWaitReturnsOnConclusion(t *testing.T) {
	url, svc := setup(t)
	p := core.Anonymous()
	for _, a := range []string{"alice", "bob"} {
		if _, err := svc.Conclude(t.Context(), p, "default", "c", core.ConcludeParams{Agent: a, Body: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Read(t.Context(), p, "default", "c", core.ReadParams{Agent: "alice"}); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t.Context(), url, "wait", "--agent", "alice", "c")
	if r.code != exitOK || !strings.HasPrefix(r.stdout, "c concluded\n") || !strings.Contains(r.stdout, "next: done") {
		t.Fatalf("wait = %+v", r)
	}
}

func TestWaitTimeout(t *testing.T) {
	url, _ := setup(t)
	r := runCLI(t.Context(), url, "wait", "c", "--agent", "alice", "--timeout", "1500ms")
	if r.code != exitTimedOut || !strings.HasPrefix(r.stdout, "no new messages before the timeout") {
		t.Fatalf("wait = %+v", r)
	}
}

func TestWaitErrorsAndUsage(t *testing.T) {
	url, _ := setup(t)
	tests := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"not a participant", []string{"wait", "c", "--agent", "zed"}, exitError, "not_participant"},
		{"no conversation", []string{"wait", "--agent", "alice"}, exitUsage, "conversation name is required"},
		{"no agent", []string{"wait", "c"}, exitUsage, "agent name is required"},
		{"extra argument", []string{"wait", "c", "d", "--agent", "alice"}, exitUsage, "unexpected argument"},
		{"bad url", []string{"wait", "c", "--agent", "alice", "--url", "ftp://x"}, exitUsage, "invalid server URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCLI(t.Context(), url, tt.args...)
			if r.code != tt.code || !strings.Contains(r.stderr, tt.msg) {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestWaitRetriesWhileServerIsDown(t *testing.T) {
	h, svc := newHandler(t)
	p := core.Anonymous()
	if _, err := svc.Start(t.Context(), p, "default", core.StartParams{Name: "c", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(t.Context(), p, "default", "c", core.JoinParams{Agent: "bob"}); err != nil {
		t.Fatal(err)
	}

	// Reserve an address, release it, and start the server there later.
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan cliResult, 1)
	go func() {
		done <- runCLI(context.Background(), "http://"+addr, "wait", "c", "--agent", "alice", "--timeout", "20s")
	}()
	time.Sleep(1500 * time.Millisecond)

	ln, err = lc.Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	select {
	case r := <-done:
		// alice never read, so bob's join is already new to her.
		if r.code != exitOK || !strings.Contains(r.stderr, "retrying") || !strings.Contains(r.stdout, "new in c") {
			t.Fatalf("wait = %+v", r)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wait never recovered")
	}
}

func TestWaitTimeoutBoundsAHungServer(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hung.Close)
	start := time.Now()
	r := runCLI(t.Context(), hung.URL, "wait", "c", "--agent", "alice", "--timeout", "2s")
	if r.code != exitTimedOut || time.Since(start) > 5*time.Second {
		t.Fatalf("wait = %+v after %s", r, time.Since(start))
	}
}

func TestWaitTimeoutBoundsBackoff(t *testing.T) {
	start := time.Now()
	r := runCLI(t.Context(), "http://127.0.0.1:1", "wait", "c", "--agent", "alice", "--timeout", "2500ms")
	if r.code != exitTimedOut || time.Since(start) > 3*time.Second {
		t.Fatalf("wait = %+v after %s", r, time.Since(start))
	}
}

func TestWaitShortTimeoutStillChecksOnce(t *testing.T) {
	url, svc := setup(t)
	if _, err := svc.Say(t.Context(), core.Anonymous(), "default", "c", core.SayParams{Agent: "bob", Body: "ping"}); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t.Context(), url, "wait", "c", "--agent", "alice", "--timeout", "300ms")
	if r.code != exitOK || !strings.HasPrefix(r.stdout, "1 new in c") {
		t.Fatalf("wait = %+v", r)
	}
}

func TestTUIUsage(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"tui", "bad name"}, "invalid target"},
		{[]string{"tui", "default/bad name"}, "invalid target"},
		{[]string{"tui", "a", "b"}, `unexpected argument "b"`},
		{[]string{"tui", "--as", "bad name"}, "invalid observer name"},
		{[]string{"tui", "--theme-dir", "/does/not/exist"}, "theme dir"},
	} {
		r := runCLI(t.Context(), "http://127.0.0.1:1", tt.args...)
		if r.code == exitOK || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("%v = %d %q, want an error containing %q", tt.args, r.code, r.stderr, tt.want)
		}
	}
}
