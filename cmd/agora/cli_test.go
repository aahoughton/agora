package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
	code := run(ctx, args, strings.NewReader(""), &out, &errOut, env)
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

func runCLIInput(ctx context.Context, url, stdin string, env map[string]string, args ...string) cliResult {
	var out, errOut bytes.Buffer
	getenv := func(k string) string {
		if k == "AGORA_URL" {
			return url
		}
		return env[k]
	}
	code := run(ctx, args, strings.NewReader(stdin), &out, &errOut, getenv)
	return cliResult{code, out.String(), errOut.String()}
}

func TestHookStop(t *testing.T) {
	url, _ := setup(t)
	// In setup, alice has read and bob has not: bob is expected in c.
	tests := []struct {
		name, stdin string
		env         map[string]string
		block       bool
	}{
		{"expected", `{"stop_hook_active":false}`, map[string]string{"AGORA_AGENT": "bob"}, true},
		{"not expected", `{}`, map[string]string{"AGORA_AGENT": "carol"}, false},
		{"no agent", `{}`, nil, false},
		{"already continuing", `{"stop_hook_active":true}`, map[string]string{"AGORA_AGENT": "bob"}, false},
		{"bad input", `not json`, map[string]string{"AGORA_AGENT": "bob"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCLIInput(t.Context(), url, tt.stdin, tt.env, "hook", "stop")
			if r.code != exitOK {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			if !tt.block {
				if r.stdout != "" {
					t.Fatalf("blocked: %s", r.stdout)
				}
				return
			}
			var out struct{ Decision, Reason string }
			if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
				t.Fatalf("stdout %q: %v", r.stdout, err)
			}
			if out.Decision != "block" || !strings.Contains(out.Reason, "c: open; you: active, expected") {
				t.Fatalf("hook output = %+v", out)
			}
		})
	}
	t.Run("server down", func(t *testing.T) {
		r := runCLIInput(t.Context(), "http://127.0.0.1:1", `{}`, map[string]string{"AGORA_AGENT": "bob"}, "hook", "stop")
		if r.code != exitOK || r.stdout != "" || !strings.Contains(r.stderr, "agora hook stop:") {
			t.Fatalf("server down = %+v", r)
		}
	})
}

func TestHookStopPatience(t *testing.T) {
	url, svc := setup(t)
	p := core.Anonymous()
	cache := t.TempDir()
	env := map[string]string{"AGORA_AGENT": "bob", "XDG_CACHE_HOME": cache}
	stop := func(stdin string, args ...string) bool {
		t.Helper()
		r := runCLIInput(t.Context(), url, stdin, env, append([]string{"hook", "stop"}, args...)...)
		if r.code != exitOK {
			t.Fatalf("exit %d: %s", r.code, r.stderr)
		}
		return strings.Contains(r.stdout, `"decision":"block"`)
	}
	const first = `{"session_id":"s1","stop_hook_active":false}`
	const again = `{"session_id":"s1","stop_hook_active":true}`

	// A session that keeps stopping without calling in is held three times,
	// then let go, and stays let go until it makes progress.
	for i, want := range []bool{true, true, true, false, false} {
		in := again
		if i == 0 {
			in = first
		}
		if got := stop(in); got != want {
			t.Fatalf("stop %d: blocked = %v, want %v", i+1, got, want)
		}
	}
	// A new message is progress: the hook holds the session again.
	if _, err := svc.Say(t.Context(), p, "default", "c", core.SayParams{Agent: "alice", Body: "still there?"}); err != nil {
		t.Fatal(err)
	}
	if !stop(again) {
		t.Fatal("not blocked after a new message")
	}
	// So is calling in, such as a wait that finds nothing new.
	for range 3 {
		if _, err := svc.Wait(t.Context(), p, "default", "c", core.WaitParams{Agent: "bob", Timeout: time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		if !stop(again) {
			t.Fatal("not blocked after calling wait")
		}
	}
	// Another session is counted separately.
	if !stop(`{"session_id":"s2","stop_hook_active":true}`) {
		t.Fatal("other session not blocked")
	}
	// Once bob concludes he is not expected: the stop is allowed and the
	// session's state is gone.
	if _, err := svc.Conclude(t.Context(), p, "default", "c", core.ConcludeParams{Agent: "bob", Body: "done"}); err != nil {
		t.Fatal(err)
	}
	if stop(again) {
		t.Fatal("blocked when not expected")
	}
	files, err := os.ReadDir(filepath.Join(cache, "agora", "stop-hook"))
	if err != nil || len(files) != 1 {
		t.Fatalf("state files = %v, %v; want only s2's", files, err)
	}

	t.Run("patience flag", func(t *testing.T) {
		env["AGORA_AGENT"] = "alice"
		if !stop(`{"session_id":"s3"}`, "--patience", "1") || stop(`{"session_id":"s3"}`, "--patience", "1") {
			t.Fatal("--patience 1 did not block exactly once")
		}
		if r := runCLIInput(t.Context(), url, `{}`, env, "hook", "stop", "--patience", "0"); r.code != exitUsage {
			t.Fatalf("--patience 0 = %+v", r)
		}
	})
}

func TestMCPConfig(t *testing.T) {
	r := runCLI(t.Context(), "http://127.0.0.1:9000/", "mcp-config", "--profile", "codex", "--namespace", "team-a", "--stop-hook")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		`url = "http://127.0.0.1:9000/mcp?namespace=team-a"`,
		`default_tools_approval_mode = "approve"`,
		`"command": "agora hook stop"`,
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q in:\n%s", want, r.stdout)
		}
	}
	for _, args := range [][]string{{"mcp-config"}, {"mcp-config", "--profile", "gemini"}} {
		if r := runCLI(t.Context(), "http://127.0.0.1:9000", args...); r.code != exitUsage {
			t.Errorf("%v = %d, want usage error", args, r.code)
		}
	}
}
