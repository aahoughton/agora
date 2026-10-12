package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/server"
)

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
		{"no args", nil, 2, "", "usage: agora"},
		{"help", []string{"help"}, 0, "usage: agora", ""},
		{"version", []string{"version"}, 0, "dev\n", ""},
		{"unknown", []string{"bogus"}, 2, "", `unknown command "bogus"`},
		{"serve bad flag", []string{"serve", "--auth", "ldap"}, 2, "", "unknown auth mode"},
		{"serve help", []string{"serve", "-h"}, 0, "", "-db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), tt.args, &out, &errOut, testEnv)
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
	cfg := config.Serve{DB: filepath.Join(t.TempDir(), "agora.db"), Addr: "127.0.0.1:0", Auth: config.AuthNone}

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
	cfg := config.Serve{DB: filepath.Join(t.TempDir(), "agora.db"), Addr: "127.0.0.1:0", Auth: config.AuthNone}
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
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/c/wait?agent=alice&after=1000&timeout=300", nil)
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
		name string
		cfg  config.Serve
		addr net.Addr
		want []string
	}{
		{"loopback default", config.Serve{}, loop, server.LoopbackHosts},
		{"wildcard default", config.Serve{}, wild, nil},
		{"explicit wins", config.Serve{AllowedHosts: []string{"agora.lan"}}, wild, []string{"agora.lan"}},
		{"star wins on loopback", config.Serve{AnyHost: true}, loop, nil},
	}
	for _, tt := range tests {
		if got := allowedHosts(tt.cfg, tt.addr); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
