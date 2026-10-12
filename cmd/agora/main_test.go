package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/config"
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
	cfg := config.Serve{DB: "/unused.db", Addr: "127.0.0.1:0", Auth: config.AuthNone}

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
