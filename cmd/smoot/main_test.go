package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/auth"
	"github.com/aahoughton/smoot/internal/config"
	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/server"
	"github.com/aahoughton/smoot/internal/store"
)

// TestMain gives the package a scratch HOME, so commands that look for a
// default token file never read the developer's. Started through a link
// named smoot, the test binary runs as the CLI instead, so tests can launch
// the commands an installed client config names.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "smoot" {
		os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
	}
	home, err := os.MkdirTemp("", "smoot-test-home")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func testEnv(k string) string {
	if k == "HOME" {
		return "/home/test"
	}
	return ""
}

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args", nil, 2, "", "usage: smoot"},
		{"help", []string{"help"}, 0, "usage: smoot", ""},
		{"version", []string{"version"}, 0, "dev\n", ""},
		{"unknown", []string{"bogus"}, 2, "", `unknown command "bogus"`},
		{"serve bad flag", []string{"serve", "--auth", "ldap"}, 2, "", "unknown auth mode"},
		{"serve help", []string{"serve", "-h"}, 0, "", "usage: smoot serve"},
		{"version flag", []string{"--version"}, 0, "dev\n", ""},
		{"version help", []string{"version", "-h"}, 0, "usage: smoot version", ""},
		{"version argument", []string{"version", "x"}, 2, "", "unexpected argument"},
		{"status help", []string{"status", "-h"}, 0, "", "usage: smoot status"},
		{"health help", []string{"health", "-h"}, 0, "", "usage: smoot health"},
		{"health argument", []string{"health", "x"}, 2, "", "unexpected argument"},
		{"client help", []string{"client", "-h"}, 0, "usage: smoot client", ""},
		{"client alone", []string{"client"}, 2, "", "usage: smoot client"},
		{"client unknown", []string{"client", "remove"}, 2, "", `unknown command "remove"`},
		{"client config help", []string{"client", "config", "-h"}, 0, "", "usage: smoot client config NAME"},
		{"old mcp-config", []string{"mcp-config", "--profile", "claude"}, 2, "", `unknown command "mcp-config"`},
		{"old skills install", []string{"skills", "install"}, 2, "", `unknown command "skills"`},
		{"hook help", []string{"hook", "--help"}, 0, "usage: smoot hook stop", ""},
		{"admin leaf help", []string{"admin", "tokens", "issue", "-h"}, 0, "", "usage: smoot admin tokens issue ACCOUNT"},
		{"admin status help", []string{"admin", "status", "-h"}, 0, "", "usage: smoot admin status"},
		{"db bootstrap help", []string{"db", "bootstrap", "-h"}, 0, "", "usage: smoot db bootstrap --db PATH"},
		{"db help", []string{"db", "help"}, 0, "usage: smoot db", ""},
		{"db alone", []string{"db"}, 2, "", "usage: smoot db"},
		{"db load argument", []string{"db", "load", "--db", "x.db", "extra"}, 2, "", `unexpected argument "extra"`},
		{"db migrate argument", []string{"db", "migrate", "--db", "x.db", "extra"}, 2, "", `unexpected argument "extra"`},
		{"old migrate", []string{"migrate", "--db", "x"}, 2, "", `unknown command "migrate"`},
		{"old admin bootstrap", []string{"admin", "bootstrap", "--db", "x", "--name", "n"}, 2, "", `unknown command "bootstrap"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), tt.args, strings.NewReader(""), &out, &errOut, testEnv)
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if !strings.Contains(out.String(), tt.wantStdout) {
				t.Fatalf("stdout = %q, want containing %q", out.String(), tt.wantStdout)
			}
			if !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want containing %q", errOut.String(), tt.wantStderr)
			}
		})
	}
}

func TestServeStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := config.Serve{DB: filepath.Join(t.TempDir(), "smoot.db"), Addr: "127.0.0.1:0", Auth: config.AuthNone}

	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, logger, ready) }()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("serve exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
	if !strings.Contains(logs.String(), "auth is off") {
		t.Fatalf("missing auth warning in logs: %s", logs.String())
	}
}

func TestShutdownDoesNotWaitForLongPolls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Serve{DB: filepath.Join(t.TempDir(), "smoot.db"), Addr: "127.0.0.1:0", Auth: config.AuthNone}
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, slog.New(slog.DiscardHandler), ready) }()
	addr := <-ready
	base := "http://" + addr + "/v1/namespaces/default/conversations"

	post := func(path, body string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode >= 300 {
			t.Fatalf("POST %s = %d", path, resp.StatusCode)
		}
	}
	post("", `{"name":"c","agent":"alice","end":"agreement"}`)

	waitErr := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/c/wait", strings.NewReader(`{"agent":"alice","after":1000,"timeout_seconds":300}`))
		if err != nil {
			waitErr <- err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			err = resp.Body.Close()
		}
		waitErr <- err
	}()
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked on a long poll")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
	<-waitErr
}

func TestAllowedHostsResolution(t *testing.T) {
	loop := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8787}
	wild := &net.TCPAddr{IP: net.IPv4zero, Port: 8787}
	tests := []struct {
		name    string
		cfg     config.Serve
		addr    net.Addr
		want    []string
		refused bool
	}{
		{"loopback default", config.Serve{Auth: config.AuthNone}, loop, server.LoopbackHosts, false},
		{"wildcard default with tokens", config.Serve{Auth: config.AuthToken}, wild, nil, false},
		// Anyone's browser could reach an open server by rebinding a name.
		{"wildcard default with auth off", config.Serve{Auth: config.AuthNone}, wild, nil, true},
		{"explicit wins", config.Serve{Auth: config.AuthNone, AllowedHosts: []string{"smoot.lan"}}, wild, []string{"smoot.lan"}, false},
		{"star wins on loopback", config.Serve{AnyHost: true}, loop, nil, false},
		{"star allows any host with auth off", config.Serve{Auth: config.AuthNone, AnyHost: true}, wild, nil, false},
	}
	for _, tt := range tests {
		got, err := allowedHosts(tt.cfg, tt.addr)
		if (err != nil) != tt.refused || !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, %v", tt.name, got, err)
		}
	}
}

// TestEndedBrowserSessionsAreNotFailedSignIns: a tab left open on an ended
// session keeps refreshing with its cookie. It is sent to sign in each
// time, and uses none of its address's allowance for bad tokens.
func TestEndedBrowserSessionsAreNotFailedSignIns(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dbPath := filepath.Join(t.TempDir(), "smoot.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.New(db, core.Options{Policy: core.RolePolicy()}).Bootstrap(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Serve{DB: dbPath, Addr: "127.0.0.1:0", Auth: config.AuthToken, Web: true, AuthFailureLimit: 2}
	ready, done := make(chan string, 1), make(chan error, 1)
	go func() { done <- serve(ctx, cfg, slog.New(slog.DiscardHandler), ready) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("serve exited early: %v", err)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, set func(*http.Request)) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		set(req)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for range 5 {
		code := get("/n/default", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: core.SessionPrefix + "ended"})
			r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // as from a proxy in front
		})
		if code != http.StatusSeeOther {
			t.Fatalf("ended session = %d, want a redirect to sign in", code)
		}
	}
	if code := get("/v1/namespaces", func(r *http.Request) { r.Header.Set("Authorization", "Bearer smoot_wrong") }); code != http.StatusUnauthorized {
		t.Fatalf("first bad token after ended sessions = %d", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeWithTokens(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dbPath := filepath.Join(t.TempDir(), "smoot.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := core.New(db, core.Options{Policy: core.RolePolicy()}).Bootstrap(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Serve{DB: dbPath, Addr: "127.0.0.1:0", Auth: config.AuthToken}
	ready, done := make(chan string, 1), make(chan error, 1)
	go func() { done <- serve(ctx, cfg, slog.New(slog.DiscardHandler), ready) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("serve exited early: %v", err)
	}
	get := func(path, token string) (int, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	if code, _ := get("/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz = %d", code)
	}
	code, h := get("/v1/namespaces", "")
	if code != http.StatusUnauthorized || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("no token: %d %v", code, h)
	}
	if code, _ := get("/v1/namespaces", tok.Secret); code != http.StatusOK {
		t.Fatalf("with token: %d", code)
	}
	if code, _ := get("/mcp", ""); code != http.StatusUnauthorized {
		t.Fatalf("mcp without token: %d", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
