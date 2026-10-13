package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/aahoughton/agora/internal/client"
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
	// The server never answers; with a request body it does not read, it
	// also never sees the client go, so the test stops it.
	stop := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(stop) })
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

func TestSkillsInstall(t *testing.T) {
	dir := t.TempDir()
	install := func(args ...string) cliResult {
		return runCLI(t.Context(), "http://127.0.0.1:1", append([]string{"skills", "install", "--dir", dir}, args...)...)
	}
	r := install()
	if r.code != exitOK || !strings.Contains(r.stdout, filepath.Join(dir, "agora", "SKILL.md")) || !strings.Contains(r.stdout, filepath.Join(dir, "convene", "SKILL.md")) {
		t.Fatalf("first install = %+v", r)
	}
	if r := install(); r.code != exitOK || !strings.Contains(r.stdout, "up to date") {
		t.Fatalf("second install = %+v", r)
	}

	// A changed file blocks the install, and nothing else is written.
	edited := filepath.Join(dir, "agora", "SKILL.md")
	if err := os.WriteFile(edited, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "convene", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if r := install(); r.code != exitError || !strings.Contains(r.stderr, edited) {
		t.Fatalf("conflicting install = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "convene", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused install wrote files")
	}
	if r := install("--force"); r.code != exitOK {
		t.Fatalf("forced install = %+v", r)
	}
	if got, _ := os.ReadFile(edited); string(got) == "mine" {
		t.Fatal("--force did not replace the file")
	}

	if r := runCLI(t.Context(), "", "skills", "install"); r.code != exitUsage {
		t.Fatalf("no client = %+v", r)
	}
}

// tokenServer serves agora in auth mode token and returns its URL and the
// bootstrapped server admin's token.
func tokenServer(t *testing.T) (string, string, *core.Service) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := core.New(db, core.Options{Policy: core.RolePolicy()})
	tok, err := svc.Bootstrap(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(server.Deps{
		Service: svc, Authenticator: auth.Bearer{Service: svc}, Logger: slog.New(slog.DiscardHandler),
		AuthMode: "token", Version: "test", Activity: auth.NewActivity(nil, nil),
	}))
	t.Cleanup(srv.Close)
	return srv.URL, tok.Secret, svc
}

func TestClientsSendTheToken(t *testing.T) {
	url, token, _ := tokenServer(t)
	with := map[string]string{"AGORA_TOKEN": token}

	r := runCLIInput(t.Context(), url, "", with, "whoami")
	if r.code != exitOK || !strings.Contains(r.stdout, "auth token") || !strings.Contains(r.stdout, "account: root (server admin)") {
		t.Fatalf("whoami = %+v", r)
	}
	if r := runCLIInput(t.Context(), url, "", nil, "whoami"); r.code != exitError || !strings.Contains(r.stderr, "unauthorized") {
		t.Fatalf("whoami without a token = %+v", r)
	}
	if r := runCLIInput(t.Context(), url, "", with, "status", "--agent", "a"); r.code != exitOK {
		t.Fatalf("status = %+v", r)
	}
	// --token beats the environment.
	if r := runCLIInput(t.Context(), url, "", with, "whoami", "--token", "agora_wrong"); r.code != exitError {
		t.Fatalf("whoami with a bad --token = %+v", r)
	}
}

func TestWhoamiWithAuthOff(t *testing.T) {
	url, _ := setup(t)
	r := runCLI(t.Context(), url, "whoami")
	if r.code != exitOK || !strings.Contains(r.stdout, "account: none (auth is off") {
		t.Fatalf("whoami = %+v", r)
	}
}

func TestAdminBootstrapAndRecover(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	if r := runCLI(t.Context(), "", "admin", "bootstrap", "--db", missing, "--name", "root"); r.code != exitError {
		t.Fatalf("bootstrap on a missing file = %+v", r)
	}
	path := filepath.Join(dir, "agora.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	r := runCLI(t.Context(), "", "admin", "bootstrap", "--db", path, "--name", "root")
	if r.code != exitOK || !strings.Contains(r.stdout, "root is a server admin") || !strings.Contains(r.stdout, core.TokenPrefix) {
		t.Fatalf("bootstrap = %+v", r)
	}
	if r := runCLI(t.Context(), "", "admin", "bootstrap", "--db", path, "--name", "again"); r.code != exitError || !strings.Contains(r.stderr, "accounts already exist") {
		t.Fatalf("second bootstrap = %+v", r)
	}
	if r := runCLI(t.Context(), "", "admin", "recover", "--db", path, "--name", "root"); r.code != exitOK || !strings.Contains(r.stdout, core.TokenPrefix) {
		t.Fatalf("recover = %+v", r)
	}
	if r := runCLI(t.Context(), "", "admin", "recover", "--db", path); r.code != exitUsage {
		t.Fatalf("recover without --name = %+v", r)
	}
}

func TestAdminThroughTheServer(t *testing.T) {
	url, token, _ := tokenServer(t)
	admin := func(args ...string) cliResult {
		return runCLIInput(t.Context(), url, "", map[string]string{"AGORA_TOKEN": token}, append([]string{"admin"}, args...)...)
	}
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"accounts", "add", "pat"}, "pat "},
		{[]string{"accounts", "add", "boss", "--server-admin"}, "boss  yes"},
		{[]string{"accounts", "disable", "pat"}, "pat   no            yes"},
		{[]string{"accounts", "enable", "pat"}, "pat   no            no"},
		{[]string{"accounts", "list"}, "root"},
		{[]string{"namespaces", "create", "team"}, "created namespace team"},
		{[]string{"members", "set", "team", "pat", "member"}, "pat is member in team"},
		{[]string{"members", "list", "team"}, "pat      member"},
		{[]string{"tokens", "issue", "pat", "--label", "laptop", "--ttl", "720h"}, "for pat (shown once):\n" + core.TokenPrefix},
		{[]string{"tokens", "list", "pat"}, "laptop"},
		{[]string{"tokens", "revoke-all", "pat"}, "revoked 1 tokens of pat"},
		{[]string{"members", "remove", "team", "pat"}, "pat has no role in team"},
	}
	for _, s := range steps {
		r := admin(s.args...)
		if r.code != exitOK || !strings.Contains(r.stdout, s.want) {
			t.Fatalf("admin %v = %+v, want %q", s.args, r, s.want)
		}
	}
	if r := admin("accounts", "demote", "root"); r.code != exitOK {
		t.Fatalf("demote with another admin left = %+v", r)
	}
	if r := admin("members", "set", "team"); r.code != exitUsage || !strings.Contains(r.stderr, "takes 3 arguments") {
		t.Fatalf("missing arguments = %+v", r)
	}
	if r := admin("accounts", "rename", "x"); r.code != exitUsage {
		t.Fatalf("unknown command = %+v", r)
	}
	if r := runCLIInput(t.Context(), url, "", nil, "admin", "accounts", "list"); r.code != exitError || !strings.Contains(r.stderr, "unauthorized") {
		t.Fatalf("no token = %+v", r)
	}
}

func TestMCPConfigNeverPrintsTheToken(t *testing.T) {
	env := map[string]string{"AGORA_TOKEN": "agora_supersecret"}
	for _, profile := range []string{"claude", "codex"} {
		r := runCLIInput(t.Context(), "http://127.0.0.1:9000", "", env, "mcp-config", "--profile", profile, "--with-token", "--stop-hook")
		if r.code != exitOK || strings.Contains(r.stdout, "supersecret") || !strings.Contains(r.stdout, "AGORA_TOKEN") {
			t.Fatalf("%s: %+v", profile, r)
		}
	}
}

func TestTUIChecksItsToken(t *testing.T) {
	url, _, _ := tokenServer(t)
	r := runCLIInput(t.Context(), url, "", map[string]string{"AGORA_TOKEN": "agora_wrong"}, "tui")
	if r.code != exitError || !strings.Contains(r.stderr, "unauthorized") {
		t.Fatalf("tui with a bad token = %+v", r)
	}
}

func TestBackupDumpAndLoad(t *testing.T) {
	url, token, svc := tokenServer(t)
	admin, err := svc.AuthenticateToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(t.Context(), admin, "default", core.StartParams{Name: "c", Agent: "a", Join: true}); err != nil {
		t.Fatal(err)
	}
	with := map[string]string{"AGORA_TOKEN": token}
	dir := t.TempDir()
	cli := func(env map[string]string, args ...string) cliResult {
		return runCLIInput(t.Context(), url, "", env, args...)
	}

	backup := filepath.Join(dir, "backup.db")
	if r := cli(nil, "backup", "-o", backup); r.code != exitError {
		t.Fatalf("backup without a token = %+v", r)
	}
	if r := cli(with, "backup", "-o", backup); r.code != exitOK {
		t.Fatalf("backup = %+v", r)
	}
	if info, err := os.Stat(backup); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", info, err)
	}
	if r := cli(with, "backup", "-o", backup); r.code != exitError || !strings.Contains(r.stderr, "exists") {
		t.Fatalf("backup over an existing file = %+v", r)
	}
	db, err := store.Open(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	var convs int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM conversations").Scan(&convs); err != nil || convs != 1 {
		t.Fatalf("backup holds %d conversations, %v", convs, err)
	}
	_ = db.Close()

	for _, bad := range [][]string{{"dump", "-o", "x"}, {"dump", "--all", "--namespace", "default", "-o", "x"}, {"dump", "--all"}} {
		if r := cli(with, bad...); r.code != exitUsage {
			t.Fatalf("%v = %+v", bad, r)
		}
	}
	full, part := filepath.Join(dir, "full.jsonl"), filepath.Join(dir, "default.jsonl")
	if r := cli(with, "dump", "--all", "-o", full); r.code != exitOK {
		t.Fatalf("dump --all = %+v", r)
	}
	if r := cli(with, "dump", "--namespace", "default", "-o", part); r.code != exitOK {
		t.Fatalf("dump --namespace = %+v", r)
	}

	loaded := filepath.Join(dir, "loaded.db")
	if r := cli(nil, "load", "--db", loaded, "-i", full); r.code != exitOK || !strings.Contains(r.stdout, "loaded") {
		t.Fatalf("load = %+v", r)
	}
	if r := cli(nil, "load", "--db", loaded, "-i", part); r.code != exitError || !strings.Contains(r.stderr, "exists") {
		t.Fatalf("load over an existing database = %+v", r)
	}
	if r := cli(nil, "load", "--db", filepath.Join(dir, "ns.db"), "-i", part); r.code != exitOK || !strings.Contains(r.stdout, "namespace default") {
		t.Fatalf("load namespace dump = %+v", r)
	}

	// Without --db, a namespace dump goes into the running server.
	if r := cli(with, "load", "-i", part, "--as", "copy"); r.code != exitOK || !strings.Contains(r.stdout, "loaded namespace copy into") {
		t.Fatalf("load into the server = %+v", r)
	}
	if r := cli(with, "load", "-i", part, "--as", "copy"); r.code != exitError || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("load into an existing namespace = %+v", r)
	}
	if r := cli(with, "load", "-i", full); r.code != exitError || !strings.Contains(r.stderr, "full dump") {
		t.Fatalf("load a full dump into the server = %+v", r)
	}
	if r := cli(nil, "load", "--db", filepath.Join(dir, "as.db"), "--as", "x", "-i", part); r.code != exitUsage {
		t.Fatalf("--as with --db = %+v", r)
	}

	// A cut-off dump is refused, and no database is left behind.
	raw, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimRight(string(raw), "\n"), "\n")
	cut := filepath.Join(dir, "cut.jsonl")
	if err := os.WriteFile(cut, []byte(strings.Join(lines[:len(lines)-1], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "cut.db")
	if r := cli(nil, "load", "--db", target, "-i", cut); r.code != exitError || !strings.Contains(r.stderr, "cut off") {
		t.Fatalf("load of a cut-off dump = %+v", r)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("a failed load left %s: %v", target, err)
	}
}

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agora.db")
	if r := runCLI(t.Context(), "", "migrate", "--db", path, "--check"); r.code != exitError {
		t.Fatalf("migrate on a missing file = %+v", r)
	}
	// An old database: schema 1.
	db, err := store.OpenAt(t.Context(), path, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if r := runCLI(t.Context(), "", "migrate", "--db", path, "--check"); r.code != exitPending || !strings.Contains(r.stdout, "pending") {
		t.Fatalf("check = %+v", r)
	}
	if r := runCLI(t.Context(), "", "migrate", "--db", path); r.code != exitOK || !strings.Contains(r.stdout, "migrated from schema 1") {
		t.Fatalf("migrate = %+v", r)
	}
	if _, err := os.Stat(path + ".pre-v1"); err != nil {
		t.Fatalf("no snapshot: %v", err)
	}
	if r := runCLI(t.Context(), "", "migrate", "--db", path, "--check"); r.code != exitOK || !strings.Contains(r.stdout, "current") {
		t.Fatalf("check after = %+v", r)
	}
}

// Only serve and migrate change a database's schema; admin commands refuse
// an out-of-date database instead of migrating it unseen.
func TestAdminStatus(t *testing.T) {
	url, token, svc := tokenServer(t)
	env := map[string]string{"AGORA_TOKEN": token}
	root, err := svc.AuthenticateToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(t.Context(), root, "default", core.StartParams{Name: "c", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	r := runCLIInput(t.Context(), url, "", env, "admin", "status")
	if r.code != exitOK {
		t.Fatalf("status = %+v", r)
	}
	for _, want := range []string{
		"(version test, auth token)",
		"last message now: default/c #",
		"default/c     open    now   1       0        0",
		"No open MCP sessions.",
		"root     api      127.0.0.1  agora-cli",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, r.stdout)
		}
	}

	r = runCLIInput(t.Context(), url, "", env, "admin", "status", "--json")
	var st client.ServerStatus
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil || st.AuthMode != "token" || len(st.Callers) != 1 {
		t.Fatalf("status --json = %+v (%v)", r, err)
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                            "now",
		42 * time.Second:             "42s ago",
		5*time.Minute + time.Second:  "5m ago",
		2*time.Hour + 13*time.Minute: "2h13m ago",
		50 * time.Hour:               "2d2h ago",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAdminRefusesAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	db, err := store.OpenAt(t.Context(), path, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	r := runCLI(t.Context(), "", "admin", "bootstrap", "--db", path, "--name", "root")
	if r.code != exitError || !strings.Contains(r.stderr, "agora migrate") {
		t.Fatalf("bootstrap on an old database = %+v", r)
	}
	if v, err := store.Inspect(t.Context(), path); err != nil || v != 1 {
		t.Fatalf("schema now %d, %v", v, err)
	}
}
